//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/sso"
	oidcclient "github.com/grepnest/grepnest/internal/sso/oidc"
)

func TestBreakGlassRecoveryAcrossReplicas(t *testing.T) {
	database := newMilestoneDatabase(t)
	seedOIDCAuthorization(t, database)
	idp := newOIDCTestProvider(t)
	public := newOIDCPublicServer(t)
	a := newBreakGlassReplica(t, database, idp, public.URL, true)
	b := newBreakGlassReplica(t, database, idp, public.URL, true)
	public.Config.Handler = replicaHandler(a, b)

	const (
		user        = "recovery-e2e"
		initial     = "initial-sentinel-password"
		rotated     = "rotated-sentinel-password"
		reset       = "reset-sentinel-password"
		final       = "final-sentinel-password"
		auditMarker = "sentinel-claims-and-body-text"
	)
	runBreakGlassCommand(t, database, user, initial)
	client := cookieClient(t, public)

	if response := localRequest(t, client, public.URL, "A", "/auth/local", map[string]string{"user_name": user, "password": initial}); response.StatusCode != http.StatusUnauthorized {
		response.Body.Close()
		t.Fatalf("forced login status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	rotate := localRequest(t, client, public.URL, "A", "/auth/local/rotate", map[string]string{"user_name": user, "current_password": initial, "new_password": rotated})
	rotate.Body.Close()
	if rotate.StatusCode != http.StatusNoContent {
		t.Fatalf("initial rotation status = %d", rotate.StatusCode)
	}

	loginClient := cookieClient(t, public)
	login := localRequest(t, loginClient, public.URL, "A", "/auth/local", map[string]string{"user_name": user, "password": rotated})
	login.Body.Close()
	if login.StatusCode != http.StatusNoContent {
		t.Fatalf("normal login status = %d", login.StatusCode)
	}
	assertSessionStatus(t, loginClient, public.URL, "B", http.StatusOK)

	runBreakGlassCommand(t, database, user, reset)
	assertSessionStatus(t, loginClient, public.URL, "B", http.StatusUnauthorized)

	recovered := cookieClient(t, public)
	response := localRequest(t, recovered, public.URL, "B", "/auth/local/rotate", map[string]string{"user_name": user, "current_password": reset, "new_password": final})
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("reset rotation status = %d", response.StatusCode)
	}
	assertOIDCLogin(t, public, "A")

	for attempt := 1; attempt <= 6; attempt++ {
		response := localRequest(t, cookieClient(t, public), public.URL, []string{"A", "B"}[(attempt-1)%2], "/auth/local", map[string]string{"user_name": "isolated-throttle-user", "password": "wrong-password-value"})
		response.Body.Close()
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
	assertAudit(t, recovered, public.URL, auditMarker, initial, rotated, reset, final)

	disabled := httptest.NewServer(newBreakGlassReplica(t, database, idp, "http://"+auditMarker+".invalid", false))
	defer disabled.Close()
	idp.server.Close()
	if response := localRequest(t, disabled.Client(), disabled.URL, "", "/auth/local", map[string]string{"user_name": user, "password": final}); response.StatusCode != http.StatusNotFound {
		response.Body.Close()
		t.Fatalf("disabled local route status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
}

func newBreakGlassReplica(t *testing.T, database milestoneDatabase, idp *oidcTestProvider, publicURL string, enabled bool) http.Handler {
	t.Helper()
	public, err := url.Parse(publicURL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := oidcclient.New(t.Context(), config.OIDC{
		IssuerURL: idp.server.URL, ClientID: "grepnest-e2e", Scopes: []string{"openid"},
		LinkClaim: "directory_id", DisplayNameClaim: "name",
	}, public, []byte("oidc-e2e-secret"), idp.caPEM())
	if err != nil {
		t.Fatal(err)
	}
	sessions := &authn.SessionManager{Store: database.store, IdleTTL: time.Hour, TTL: 2 * time.Hour, Audit: database.store}
	requestAuth := authn.RequestAuthenticator{Session: sessions, PublicOrigin: publicURL}
	mux := http.NewServeMux()
	httpapi.RegisterAuth(mux, false, enabled, []sso.Provider{&oidcclient.Provider{Client: client, Store: database.store, Sessions: sessions, LoginTTL: time.Minute}}, requestAuth, sessions, nil)
	httpapi.RegisterAdmin(mux, requestAuth, &admin.Service{Store: database.store}, 100, 64<<10, 256<<10)
	if enabled {
		local, err := authn.NewLocalAuthenticator(database.store, sessions, nil)
		if err != nil {
			t.Fatal(err)
		}
		local.Audit = database.store
		httpapi.RegisterLocalAuth(mux, publicURL, &local, database.store)
	}
	return mux
}

func replicaHandler(a, b http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Replica") == "A" {
			a.ServeHTTP(writer, request)
			return
		}
		b.ServeHTTP(writer, request)
	})
}

func runBreakGlassCommand(t *testing.T, database milestoneDatabase, userName, password string) {
	t.Helper()
	var schema string
	if err := database.pool.QueryRow(t.Context(), "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "go", "run", "../../cmd/grepnest-admin", "break-glass", "set-password", userName)
	command.Env = append(os.Environ(), "GREPNEST_DATABASE_URL="+os.Getenv("GREPNEST_TEST_POSTGRES_DSN"), "PGOPTIONS=-c search_path="+schema)
	command.Stdin = strings.NewReader(password + "\n" + password + "\n")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		t.Fatalf("grepnest-admin failed: %v", err)
	}
}

func cookieClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := *server.Client()
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func localRequest(t *testing.T, client *http.Client, baseURL, replica, path string, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", baseURL)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-Replica", replica)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertSessionStatus(t *testing.T, client *http.Client, baseURL, replica string, want int) {
	t.Helper()
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/auth/session", nil)
	request.Header.Set("X-Replica", replica)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("session status = %d, want %d", response.StatusCode, want)
	}
}

func assertOIDCLogin(t *testing.T, public *httptest.Server, replica string) {
	t.Helper()
	client := cookieClient(t, public)
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, public.URL+"/auth/oidc/login", nil)
	request.Header.Set("X-Replica", replica)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	location := response.Header.Get("Location")
	response.Body.Close()
	response, err = client.Get(location)
	if err != nil {
		t.Fatal(err)
	}
	location = response.Header.Get("Location")
	response.Body.Close()
	response, err = client.Get(location)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("OIDC callback status = %d", response.StatusCode)
	}
}

func assertAudit(t *testing.T, client *http.Client, baseURL string, sentinels ...string) {
	t.Helper()
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/admin/audit-events", nil)
	request.Header.Set("X-Replica", "A")
	response, err := client.Do(request)
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
	text := string(body)
	for _, operation := range []string{"break_glass_password_set", "password_rotated", "local_login_succeeded", "local_login_denied", "session_created", "oidc_login_succeeded"} {
		if !strings.Contains(text, operation) {
			t.Fatalf("audit missing operation %q", operation)
		}
	}
	for _, sentinel := range sentinels {
		if strings.Contains(text, sentinel) {
			t.Fatal("audit contained sentinel secret or body text")
		}
	}
}
