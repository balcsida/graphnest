//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	recoveryUser      = "recovery-e2e"
	initialPassword   = "initial-sentinel-password"
	rotatedPassword   = "rotated-sentinel-password"
	resetPassword     = "reset-sentinel-password"
	finalPassword     = "final-sentinel-password"
	oidcClaimSentinel = "oidc-claim-sentinel-never-audit"
	scimBodySentinel  = "scim-body-sentinel-never-audit"
	adminFailSentinel = "admin-failure-sentinel-password"
	scimE2EToken      = "break-glass-scim-token-32-bytes!"
	oidcClientSecret  = "break-glass-oidc-client-secret"
	webhookTestSecret = "break-glass-webhook-secret"
)

func TestBreakGlassRecoveryAcrossRealReplicas(t *testing.T) {
	database := newMilestoneDatabase(t)
	databaseURL := databaseURLForSchema(t, database)
	root := t.TempDir()
	serverBinary := buildE2EBinary(t, root, "graphnest-server", "../../cmd/graphnest-server")
	adminBinary := buildE2EBinary(t, root, "graphnest-admin", "../../cmd/graphnest-admin")
	idp := newOIDCTestProvider(t)
	idp.displayName = oidcClaimSentinel
	zoekt := newHealthyZoekt(t)
	files := writeServerSecrets(t, root, idp)

	addressA, addressB, addressDisabled := freeDualAddress(t), freeDualAddress(t), freeDualAddress(t)
	proxyA := newReplicaProxy(t, addressA, "tcp4")
	proxyB := newReplicaProxy(t, addressB, "tcp4")
	proxyBIPv6 := newReplicaProxy(t, addressB, "tcp6")
	proxyDisabled := newReplicaProxy(t, addressDisabled, "tcp4")
	publicOrigin := proxyA.URL

	replicaA := startRealServer(t, serverBinary, addressA, databaseURL, publicOrigin, idp.server.URL, zoekt.URL, files, true)
	replicaB := startRealServer(t, serverBinary, addressB, databaseURL, publicOrigin, idp.server.URL, zoekt.URL, files, true)
	disabled := startRealServer(t, serverBinary, addressDisabled, databaseURL, publicOrigin, idp.server.URL, zoekt.URL, files, false)
	t.Cleanup(func() {
		disabled.stop(t)
		replicaB.stop(t)
		replicaA.stop(t)
	})
	waitServerReady(t, proxyA.Client(), proxyA.URL, replicaA)
	waitServerReady(t, proxyB.Client(), proxyB.URL, replicaB)
	waitServerReady(t, proxyDisabled.Client(), proxyDisabled.URL, disabled)

	assertAccountThrottle(t, proxyA, proxyBIPv6, publicOrigin)
	adminOutput := runAdmin(t, adminBinary, databaseURL, recoveryUser, initialPassword, initialPassword, true)
	failedAdminOutput := runAdmin(t, adminBinary, databaseURL, recoveryUser, adminFailSentinel, "confirmation-does-not-match", false)

	forced := boundCookieClient(t, proxyA, "127.0.0.1")
	assertLocalStatus(t, forced, proxyA.URL, publicOrigin, "/auth/local", map[string]string{
		"user_name": recoveryUser, "password": initialPassword,
	}, http.StatusUnauthorized)
	assertLocalStatus(t, forced, proxyA.URL, publicOrigin, "/auth/local/rotate", map[string]string{
		"user_name": recoveryUser, "current_password": initialPassword, "new_password": rotatedPassword,
	}, http.StatusNoContent)

	local := boundCookieClient(t, proxyA, "127.0.0.1")
	assertLocalStatus(t, local, proxyA.URL, publicOrigin, "/auth/local", map[string]string{
		"user_name": recoveryUser, "password": rotatedPassword,
	}, http.StatusNoContent)
	sessionToken := sessionCookieToken(t, local, proxyA.URL)
	assertSessionStatus(t, local, proxyB.URL, http.StatusOK)

	runAdmin(t, adminBinary, databaseURL, recoveryUser, resetPassword, resetPassword, true)
	assertSessionStatus(t, local, proxyB.URL, http.StatusUnauthorized)

	recovered := boundCookieClient(t, proxyA, "127.0.0.1")
	assertLocalStatus(t, recovered, proxyB.URL, publicOrigin, "/auth/local/rotate", map[string]string{
		"user_name": recoveryUser, "current_password": resetPassword, "new_password": finalPassword,
	}, http.StatusNoContent)
	finalSessionToken := sessionCookieToken(t, recovered, proxyA.URL)

	createSCIMUser(t, proxyB.Client(), proxyB.URL)
	oidc := loginRealOIDC(t, proxyA)
	assertSessionStatus(t, oidc, proxyB.URL, http.StatusOK)
	oidcSessionToken := sessionCookieToken(t, oidc, proxyA.URL)
	apiToken := createAPIToken(t, oidc, proxyA.URL, publicOrigin)

	assertSourceThrottle(t, proxyA, proxyB, publicOrigin)

	idp.server.Close()
	assertLocalStatus(t, proxyDisabled.Client(), proxyDisabled.URL, publicOrigin, "/auth/local", map[string]string{
		"user_name": recoveryUser, "password": finalPassword,
	}, http.StatusNotFound)

	auditBody, events := readAudit(t, recovered, proxyB.URL)
	forbidden := []string{
		initialPassword, rotatedPassword, resetPassword, finalPassword,
		oidcClaimSentinel, scimBodySentinel, adminFailSentinel,
		sessionToken, finalSessionToken, oidcSessionToken, apiToken, scimE2EToken,
	}
	assertNoSentinels(t, auditBody, forbidden)
	assertNoSentinels(t, []byte(adminOutput), forbidden)
	assertNoSentinels(t, []byte(failedAdminOutput), forbidden)
	assertNoSentinels(t, []byte(replicaA.logs.String()), forbidden)
	assertNoSentinels(t, []byte(replicaB.logs.String()), forbidden)
	assertNoSentinels(t, []byte(disabled.logs.String()), forbidden)
	assertAuditEvents(t, events)
}

type serverSecretFiles struct {
	privateKey, webhook, oidcSecret, oidcCA, scimToken string
}

func writeServerSecrets(t *testing.T, root string, idp *oidcTestProvider) serverSecretFiles {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := filepath.Join(root, "github-private-key.pem")
	writeE2EFile(t, privateKey, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	webhook := filepath.Join(root, "webhook-secret")
	writeE2EFile(t, webhook, []byte(webhookTestSecret))
	oidcSecret := filepath.Join(root, "oidc-secret")
	writeE2EFile(t, oidcSecret, []byte(oidcClientSecret))
	oidcCA := filepath.Join(root, "oidc-ca.pem")
	writeE2EFile(t, oidcCA, idp.caPEM())
	scimToken := filepath.Join(root, "scim-token")
	writeE2EFile(t, scimToken, []byte(scimE2EToken))
	return serverSecretFiles{privateKey, webhook, oidcSecret, oidcCA, scimToken}
}

func writeE2EFile(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
}

func buildE2EBinary(t *testing.T, root, name, source string) string {
	t.Helper()
	path := filepath.Join(root, name)
	command := exec.CommandContext(t.Context(), "go", "build", "-o", path, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, output)
	}
	return path
}

func databaseURLForSchema(t *testing.T, database milestoneDatabase) string {
	t.Helper()
	var schema string
	if err := database.pool.QueryRow(t.Context(), "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(os.Getenv("GRAPHNEST_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("options", "-csearch_path="+schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func newHealthyZoekt(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/search" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"Result":{"Files":[]}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func newReplicaProxy(t *testing.T, targetAddress, network string) *httptest.Server {
	t.Helper()
	_, port, err := net.SplitHostPort(targetAddress)
	if err != nil {
		t.Fatal(err)
	}
	host, source := "127.0.0.1", "127.0.0.1"
	if network == "tcp6" {
		host, source = "::1", "::1"
	}
	target, err := url.Parse("http://" + net.JoinHostPort(host, port))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}}).DialContext
	proxy.Transport = transport
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writer.WriteHeader(http.StatusBadGateway)
	}
	server := httptest.NewTLSServer(proxy)
	t.Cleanup(server.Close)
	return server
}

func freeDualAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func startRealServer(t *testing.T, binary, address, databaseURL, publicOrigin, issuerURL, zoektURL string, files serverSecretFiles, breakGlass bool) *managedProcess {
	t.Helper()
	command := exec.CommandContext(t.Context(), binary)
	command.Env = append(os.Environ(),
		"GRAPHNEST_LISTEN_ADDRESS="+address,
		"GRAPHNEST_DATABASE_URL="+databaseURL,
		"GRAPHNEST_ZOEKT_URL="+zoektURL,
		"GRAPHNEST_GITHUB_WEB_URL="+issuerURL,
		"GRAPHNEST_GITHUB_API_URL="+issuerURL,
		"GRAPHNEST_GITHUB_UPLOAD_URL="+issuerURL,
		"GRAPHNEST_GITHUB_GIT_URL="+issuerURL,
		"GRAPHNEST_GITHUB_APP_ID=1",
		"GRAPHNEST_GITHUB_PRIVATE_KEY_FILE="+files.privateKey,
		"GRAPHNEST_GITHUB_WEBHOOK_SECRET_FILE="+files.webhook,
		"GRAPHNEST_GITHUB_CA_FILE="+files.oidcCA,
		"GRAPHNEST_PUBLIC_URL="+publicOrigin,
		"GRAPHNEST_OIDC_ISSUER_URL="+issuerURL,
		"GRAPHNEST_OIDC_CLIENT_ID=graphnest-e2e",
		"GRAPHNEST_OIDC_CLIENT_SECRET_FILE="+files.oidcSecret,
		"GRAPHNEST_OIDC_CA_FILE="+files.oidcCA,
		"GRAPHNEST_OIDC_SCOPES=openid",
		"GRAPHNEST_OIDC_LINK_CLAIM=directory_id",
		"GRAPHNEST_OIDC_DISPLAY_NAME_CLAIM=name",
		"GRAPHNEST_SCIM_TOKEN_FILE="+files.scimToken,
		fmt.Sprintf("GRAPHNEST_BREAK_GLASS_ENABLED=%t", breakGlass),
	)
	return startProcess(t, command)
}

func waitServerReady(t *testing.T, client *http.Client, endpoint string, process *managedProcess) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get(endpoint + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-process.done:
			t.Fatalf("server exited before readiness: %v\n%s", process.err, process.logs.String())
		case <-ctx.Done():
			t.Fatalf("server readiness: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func contextWithTimeout(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), timeout)
}

func runAdmin(t *testing.T, binary, databaseURL, userName, first, second string, success bool) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), binary, "break-glass", "set-password", userName)
	command.Env = append(os.Environ(), "GRAPHNEST_DATABASE_URL="+databaseURL)
	command.Stdin = strings.NewReader(first + "\n" + second + "\n")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if success && err != nil {
		t.Fatalf("graphnest-admin failed: %v", err)
	}
	if !success && err == nil {
		t.Fatal("graphnest-admin mismatch unexpectedly succeeded")
	}
	return output.String()
}

func boundCookieClient(t *testing.T, server *httptest.Server, sourceIP string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(sourceIP)}}).DialContext
	return &http.Client{
		Transport: transport,
		Jar:       jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func assertLocalStatus(t *testing.T, client *http.Client, endpoint, origin, path string, body any, want int) {
	t.Helper()
	response := jsonRequest(t, client, http.MethodPost, endpoint+path, origin, body)
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s status = %d, want %d", path, response.StatusCode, want)
	}
}

func jsonRequest(t *testing.T, client *http.Client, method, endpoint, origin string, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if origin != "" {
		request.Header.Set("Origin", origin)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func sessionCookieToken(t *testing.T, client *http.Client, endpoint string) string {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range client.Jar.Cookies(parsed) {
		if cookie.Name == "__Host-graphnest_session" && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatal("session cookie was not issued")
	return ""
}

func assertSessionStatus(t *testing.T, client *http.Client, endpoint string, want int) {
	t.Helper()
	response, err := client.Get(endpoint + "/v1/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("session status = %d, want %d", response.StatusCode, want)
	}
}

func createAPIToken(t *testing.T, client *http.Client, endpoint, origin string) string {
	t.Helper()
	response := jsonRequest(t, client, http.MethodPost, endpoint+"/v1/account/api-tokens", origin, map[string]any{"repository_ids": []int64{}})
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create API token status = %d", response.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body.Token == "" {
		t.Fatalf("decode API token: %v", err)
	}
	return body.Token
}

func createSCIMUser(t *testing.T, client *http.Client, endpoint string) {
	t.Helper()
	body := map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"externalId":  oidcDirectoryID,
		"userName":    "oidc-break-glass@example.test",
		"displayName": scimBodySentinel,
		"active":      true,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/scim/v2/Users", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+scimE2EToken)
	request.Header.Set("Content-Type", "application/scim+json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("SCIM create status = %d", response.StatusCode)
	}
}

func loginRealOIDC(t *testing.T, proxy *httptest.Server) *http.Client {
	t.Helper()
	client := boundCookieClient(t, proxy, "127.0.0.1")
	response, err := client.Get(proxy.URL + "/auth/oidc/login")
	if err != nil {
		t.Fatal(err)
	}
	location := response.Header.Get("Location")
	response.Body.Close()
	for step := 0; step < 2; step++ {
		response, err = client.Get(location)
		if err != nil {
			t.Fatal(err)
		}
		location = response.Header.Get("Location")
		response.Body.Close()
	}
	if response.StatusCode != http.StatusSeeOther || sessionCookieToken(t, client, proxy.URL) == "" {
		t.Fatalf("OIDC callback status = %d", response.StatusCode)
	}
	return client
}

func assertAccountThrottle(t *testing.T, a, b *httptest.Server, origin string) {
	t.Helper()
	clients := []*http.Client{
		boundCookieClient(t, a, "127.0.0.1"),
		boundCookieClient(t, b, "127.0.0.1"),
	}
	for attempt := 1; attempt <= 6; attempt++ {
		response := jsonRequest(t, clients[(attempt-1)%2], http.MethodPost, []*httptest.Server{a, b}[(attempt-1)%2].URL+"/auth/local", origin, map[string]string{
			"user_name": "account-throttle-e2e", "password": "wrong-password-value",
		})
		assertThrottleResponse(t, response, attempt)
	}
}

func assertSourceThrottle(t *testing.T, a, b *httptest.Server, origin string) {
	t.Helper()
	clients := []*http.Client{
		boundCookieClient(t, a, "127.0.0.1"),
		boundCookieClient(t, b, "127.0.0.1"),
	}
	for attempt := 1; attempt <= 6; attempt++ {
		response := jsonRequest(t, clients[(attempt-1)%2], http.MethodPost, []*httptest.Server{a, b}[(attempt-1)%2].URL+"/auth/local", origin, map[string]string{
			"user_name": fmt.Sprintf("source-throttle-e2e-%d", attempt), "password": "wrong-password-value",
		})
		assertThrottleResponse(t, response, attempt)
	}
}

func assertThrottleResponse(t *testing.T, response *http.Response, attempt int) {
	t.Helper()
	defer response.Body.Close()
	want := http.StatusUnauthorized
	if attempt == 6 {
		want = http.StatusTooManyRequests
	}
	if response.StatusCode != want {
		t.Fatalf("throttle attempt %d status = %d, want %d", attempt, response.StatusCode, want)
	}
	if attempt == 6 && response.Header.Get("Retry-After") != "900" {
		t.Fatalf("Retry-After = %q", response.Header.Get("Retry-After"))
	}
}

type auditResponse struct {
	Events    []auditEvent `json:"events"`
	Truncated bool         `json:"truncated"`
}

type auditEvent struct {
	ActorType            string `json:"actor_type"`
	ActorID              string `json:"actor_id"`
	TargetType           string `json:"target_type"`
	TargetID             string `json:"target_id"`
	AuthenticationMethod string `json:"authentication_method"`
	Operation            string `json:"operation"`
	Outcome              string `json:"outcome"`
	RequestID            string `json:"request_id"`
	CreatedAt            string `json:"created_at"`
}

func readAudit(t *testing.T, client *http.Client, endpoint string) ([]byte, []auditEvent) {
	t.Helper()
	response, err := client.Get(endpoint + "/v1/admin/audit-events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("audit status = %d", response.StatusCode)
	}
	var decoded auditResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Events) == 0 {
		t.Fatal("audit response was empty")
	}
	return body, decoded.Events
}

func assertAuditEvents(t *testing.T, events []auditEvent) {
	t.Helper()
	expected := map[string]struct {
		actor, target, method, outcome string
	}{
		"break_glass_password_set": {"operator", "user", "operator", "success"},
		"password_rotated":         {"user", "user", "local", "success"},
		"local_login_succeeded":    {"user", "session", "local", "success"},
		"local_login_denied":       {"anonymous", "authentication", "local", "denied"},
		"session_created":          {"user", "session", "", "success"},
		"oidc_login_succeeded":     {"user", "session", "oidc", "success"},
		"scim_user_created":        {"scim", "user", "scim_token", "success"},
		"api_token_created":        {"user", "api_token", "oidc", "success"},
	}
	seen := make(map[string]bool, len(expected))
	for _, event := range events {
		shape, ok := expected[event.Operation]
		if !ok {
			continue
		}
		if event.ActorType != shape.actor || event.TargetType != shape.target ||
			(shape.method != "" && event.AuthenticationMethod != shape.method) ||
			event.Outcome != shape.outcome || !validAuditIDs(event) ||
			!validAuditRequestID(event) || !validAuditTimestamp(event.CreatedAt) {
			t.Fatalf("invalid audit shape for expected operation %q", event.Operation)
		}
		if event.Operation == "session_created" && event.AuthenticationMethod != "local" && event.AuthenticationMethod != "oidc" {
			t.Fatal("invalid audit session authentication method")
		}
		seen[event.Operation] = true
	}
	missing := make([]string, 0)
	for operation := range expected {
		if !seen[operation] {
			missing = append(missing, operation)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("missing exact audit operations: %v", missing)
	}
}

func validAuditIDs(event auditEvent) bool {
	switch event.Operation {
	case "break_glass_password_set":
		return event.ActorID == "graphnest-admin" && event.TargetID == recoveryUser
	case "password_rotated":
		return positiveDecimal(event.ActorID) && event.TargetID == event.ActorID
	case "local_login_succeeded", "session_created", "oidc_login_succeeded":
		return positiveDecimal(event.ActorID) && auditSessionID(event.TargetID)
	case "local_login_denied":
		return event.ActorID == "" && event.TargetID == ""
	case "scim_user_created":
		return event.ActorID == "provisioning" && positiveDecimal(event.TargetID)
	case "api_token_created":
		return positiveDecimal(event.ActorID) && positiveDecimal(event.TargetID)
	default:
		return false
	}
}

func validAuditRequestID(event auditEvent) bool {
	if event.Operation == "break_glass_password_set" {
		return event.RequestID == ""
	}
	return event.RequestID != "" && len(event.RequestID) <= 128
}

func validAuditTimestamp(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.IsZero()
}

func positiveDecimal(value string) bool {
	number, err := strconv.ParseInt(value, 10, 64)
	return err == nil && number > 0
}

func auditSessionID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func assertNoSentinels(t *testing.T, output []byte, sentinels []string) {
	t.Helper()
	for _, sentinel := range sentinels {
		if sentinel != "" && bytes.Contains(output, []byte(sentinel)) {
			t.Fatal("captured audit or process output contained forbidden sentinel")
		}
	}
}
