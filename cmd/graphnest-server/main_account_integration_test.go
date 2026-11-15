//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/admin"
	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/config"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/graphingest"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/internal/search"
	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/balcsida/graphnest/internal/supplychain/review"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

func TestAccountGrantRevocationRecordsAudit(t *testing.T) {
	pool := serverTestPool(t)
	store := postgres.New(pool)
	var userID int64
	if err := pool.QueryRow(t.Context(), `insert into users (external_id,user_name,source) values ('account-owner','owner','scim') returning id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	client := authn.OAuthClient{ID: "gnc_account", Name: "MCP client", RedirectURIs: []string{"http://127.0.0.1/callback"}, CreatedAt: now}
	if err := store.CreateOAuthClient(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	grantID, err := store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		UserID: userID, ClientID: client.ID, AccessHash: [32]byte{1}, RefreshHash: [32]byte{2},
		CreatedAt: now, AccessExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings, endpoints, httpClient := authRuntimeSettings(t)
	settings.SSO.MCPOAuth.Enabled = true
	settings.SSO.SessionIdle = time.Hour
	settings.SSO.SessionTTL = 24 * time.Hour
	runtime, err := newAuthRuntime(t.Context(), settings, store, durableAuthenticator(store), observability.New(), endpoints, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := runtime.sessions.CreateForUser(t.Context(), userID, authn.ProviderOIDC, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := newAPIHandler(settings, observability.New(), runtime.requestAuth, nil, nil, nil, nil, nil, nil, nil, nil, nil, runtime.providers, runtime.sessions, nil, nil, runtime.mcpOAuth)
	request := httptest.NewRequest(http.MethodDelete, "/v1/account/oauth-grants/"+strconv.FormatInt(grantID, 10), nil)
	request.Header.Set("Origin", "https://graphnest.example")
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: session})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", response.Code, response.Body)
	}
	grants, _, err := store.ListOAuthGrants(t.Context(), userID, 0, 100)
	if err != nil || len(grants) != 0 {
		t.Fatalf("grants after revocation=%v err=%v", grants, err)
	}
	events, _, err := store.AuditEvents(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var revocations []audit.Event
	for _, event := range events {
		if event.Operation == audit.OperationOAuthGrantRevoked {
			revocations = append(revocations, event)
		}
	}
	if len(revocations) != 1 {
		t.Fatalf("revocation audit events=%v, want one", revocations)
	}
	event := revocations[0]
	if event.ActorType != "user" || event.ActorID != strconv.FormatInt(userID, 10) || event.TargetType != "oauth_grant" || event.TargetID != strconv.FormatInt(grantID, 10) || event.AuthenticationMethod != authn.ProviderOIDC || event.Outcome != "success" || event.RequestID == "" || event.RequestID != response.Header().Get("X-Request-ID") {
		t.Fatalf("revocation audit=%+v response request ID=%q", event, response.Header().Get("X-Request-ID"))
	}
}

// The life of an inventory-only token against the assembled server and a real
// database: an administrator mints it from a session, it pages through every
// repository (also one onboarded later) and reads a status, every other
// documented operation refuses it, and revocation or the owner's demotion
// ends it with 401.
func TestInventoryOnlyTokenListsRepositoriesUntilRevoked(t *testing.T) {
	pool := serverTestPool(t)
	store := postgres.New(pool)
	var userID int64
	if err := pool.QueryRow(t.Context(), `insert into users (external_id,user_name,source) values ('service:scip-worker','scip-worker','local') returning id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSearchNode(t.Context(), "node-a", "http://zoekt.invalid"); err != nil {
		t.Fatal(err)
	}
	onboard := func(id int64, name string, enabled bool) {
		t.Helper()
		if _, err := store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{GitHubID: id, InstallationID: 10, Owner: "acme", Name: name, CloneURL: "clone", WebURL: "https://github.example/acme/" + name, DefaultBranch: "main", SizeBytes: id * 1024, Enabled: enabled}); err != nil {
			t.Fatal(err)
		}
	}
	for id, name := range map[int64]string{101: "alpha", 102: "bravo", 103: "charlie", 104: "delta", 105: "echo"} {
		onboard(id, name, true)
	}
	onboard(106, "disabled", false)

	settings, endpoints, httpClient := authRuntimeSettings(t)
	settings.SSO.SessionIdle = time.Hour
	settings.SSO.SessionTTL = 24 * time.Hour
	settings.Limits = config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 64 << 10, MaxResults: 2, SCIPMaxUploadBytes: 1024, GraphMaxUploadBytes: 1024}
	runtime, err := newAuthRuntime(t.Context(), settings, store, durableAuthenticator(store), observability.New(), endpoints, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := runtime.sessions.CreateForUser(t.Context(), userID, authn.ProviderOIDC, false)
	if err != nil {
		t.Fatal(err)
	}
	// Every route family is mounted. Only the repository inventory has real
	// dependencies: the others must refuse the token before they need any.
	supplyChain := func(mux *http.ServeMux) {
		httpapi.RegisterSupplyChain(mux, runtime.requestAuth, &supplychain.Service{}, 10, 4096)
		httpapi.RegisterSupplyChainPortfolio(mux, runtime.requestAuth, &supplychain.Portfolio{}, 10, 4096, &httpapi.DerivedExport{Service: &supplychain.Service{}})
		httpapi.RegisterSupplyChainImports(mux, runtime.requestAuth, &supplychain.Importer{}, &httpapi.UploadGrants{}, 1024, 4096)
		httpapi.RegisterSupplyChainReview(mux, runtime.requestAuth, &review.Service{Authorizer: authz.NewPostgres(store)}, 1024, 4096)
	}
	handler := newAPIHandler(settings, observability.New(), runtime.requestAuth, &search.Service{}, &repository.Service{Store: store, GitHub: &githubapp.Client{}, SCIP: store, Graph: store},
		&scipgraph.Service{}, &graphingest.Service{Store: store}, &graphservice.Service{}, nil, nil, &admin.Service{}, nil, runtime.providers, runtime.sessions, nil, nil, nil, supplyChain)
	call := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		} else {
			request.Header.Set("Origin", "https://graphnest.example")
			request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: session})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	mint := func() (int64, string) {
		t.Helper()
		response := call(http.MethodPost, "/v1/account/inventory-tokens", "", `{}`)
		var created struct {
			ID            int64  `json:"id"`
			Token         string `json:"token"`
			InventoryOnly bool   `json:"inventory_only"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || response.Code != http.StatusCreated || !created.InventoryOnly || created.ID < 1 || created.Token == "" {
			t.Fatalf("mint status=%d body=%s err=%v", response.Code, response.Body, err)
		}
		return created.ID, created.Token
	}
	type summary struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		SizeBytes  int64  `json:"size_bytes"`
		SCIPStatus string `json:"scip_status"`
	}
	list := func(token string) ([]string, int) {
		t.Helper()
		var names []string
		for pages, cursor := 1, ""; ; pages++ {
			response := call(http.MethodGet, "/v1/repositories"+cursor, token, "")
			var page struct {
				Repositories []summary `json:"repositories"`
				Truncated    bool      `json:"truncated"`
				NextCursor   string    `json:"next_cursor"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Repositories) > 2 {
				t.Fatalf("page %d status=%d body=%s err=%v", pages, response.Code, response.Body, err)
			}
			for _, item := range page.Repositories {
				if item.SizeBytes != item.ID*1024 {
					t.Fatalf("repository %s size_bytes=%d", item.Name, item.SizeBytes)
				}
				names = append(names, item.Name)
			}
			if !page.Truncated {
				return names, pages
			}
			cursor = "?cursor=" + page.NextCursor
		}
	}

	tokenID, token := mint()
	// The token cannot mint, and neither can an ordinary administrator token.
	if response := call(http.MethodPost, "/v1/account/inventory-tokens", token, `{}`); response.Code != http.StatusForbidden {
		t.Fatalf("mint with an inventory-only token status=%d body=%s", response.Code, response.Body)
	}
	_, scoped, err := authn.TokenManager{Store: store}.CreateWithMethod(t.Context(), userID, authn.ProviderOIDC, []int64{101}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response := call(http.MethodPost, "/v1/account/inventory-tokens", scoped, `{}`); response.Code != http.StatusForbidden {
		t.Fatalf("mint with an administrator token status=%d body=%s", response.Code, response.Body)
	}

	names, pages := list(token)
	if want := []string{"acme/alpha", "acme/bravo", "acme/charlie", "acme/delta", "acme/echo"}; !slices.Equal(names, want) || pages != 3 {
		t.Fatalf("listed %v over %d pages, want %v over 3", names, pages, want)
	}
	onboard(107, "foxtrot", true)
	if names, _ := list(token); !slices.Contains(names, "acme/foxtrot") || len(names) != 6 {
		t.Fatalf("after onboarding listed %v", names)
	}
	for _, path := range []string{"/v1/repositories/107", "/v1/repositories/107/status"} {
		response := call(http.MethodGet, path, token, "")
		var status summary
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != http.StatusOK || status.Name != "acme/foxtrot" || status.SCIPStatus != "absent" {
			t.Fatalf("%s status=%d body=%s err=%v", path, response.Code, response.Body, err)
		}
	}
	if response := call(http.MethodGet, "/v1/repositories/106/status", token, ""); response.Code != http.StatusNotFound {
		t.Fatalf("disabled repository status=%d body=%s", response.Code, response.Body)
	}

	// Every other documented operation that takes a bearer token refuses it.
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	inventory := map[string]bool{"/v1/repositories": true, "/v1/repositories/{id}": true, "/v1/repositories/{id}/status": true}
	refused := 0
	for path, item := range spec.Paths {
		if item["x-graphnest-status"] == "planned" {
			continue
		}
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			operation, ok := item[method].(map[string]any)
			if !ok || !acceptsBearer(operation) || (method == "get" && inventory[path]) {
				continue
			}
			response := call(strings.ToUpper(method), pathParameter.ReplaceAllString(path, "107"), token, `{}`)
			if response.Code != http.StatusForbidden {
				t.Errorf("%s %s: status=%d body=%s, want 403", strings.ToUpper(method), path, response.Code, response.Body)
			}
			refused++
		}
	}
	// A sweep that found nothing to refuse would prove nothing.
	if refused < 60 {
		t.Fatalf("only %d documented operations were checked", refused)
	}

	response := call(http.MethodGet, "/v1/account/api-tokens", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"inventory_only":true`) {
		t.Fatalf("token list status=%d body=%s", response.Code, response.Body)
	}
	if response := call(http.MethodDelete, "/v1/account/api-tokens/"+strconv.FormatInt(tokenID, 10), "", ""); response.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", response.Code, response.Body)
	}
	if response := call(http.MethodGet, "/v1/repositories", token, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d body=%s, want 401", response.Code, response.Body)
	}

	// Demotion ends a token just as completely, although its owner keeps a
	// direct grant that an ordinary token without a ceiling would fall back to.
	_, token = mint()
	if _, err := pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 101)`, userID); err != nil {
		t.Fatal(err)
	}
	if names, _ := list(token); len(names) != 6 {
		t.Fatalf("second token listed %v", names)
	}
	if _, err := pool.Exec(t.Context(), `delete from user_roles where user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if response := call(http.MethodGet, "/v1/repositories", token, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("demoted owner's token status=%d body=%s, want 401", response.Code, response.Body)
	}

	events, _, err := store.AuditEvents(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var minted, revoked, ordinary int
	for _, event := range events {
		switch {
		case event.Operation == audit.OperationAPITokenCreated && event.AuthenticationMethod == authn.ProviderOIDC && event.RequestID != "":
			if event.TokenKind != audit.TokenKindInventoryOnly {
				t.Fatalf("session mint recorded kind %q", event.TokenKind)
			}
			minted++
		case event.Operation == audit.OperationAPITokenRevoked && event.TargetID == strconv.FormatInt(tokenID, 10):
			if event.TokenKind != audit.TokenKindInventoryOnly {
				t.Fatalf("revocation recorded kind %q", event.TokenKind)
			}
			revoked++
		case event.Operation == audit.OperationAPITokenCreated && event.TokenKind == audit.TokenKindOrdinary:
			ordinary++
		}
	}
	if minted != 2 || revoked != 1 || ordinary != 1 {
		t.Fatalf("audited mints from the session=%d revocations=%d ordinary=%d, want 2, 1 and 1", minted, revoked, ordinary)
	}
}

// acceptsBearer reports whether an OpenAPI operation lists bearerAuth among
// its security requirements.
func acceptsBearer(operation map[string]any) bool {
	requirements, _ := operation["security"].([]any)
	for _, requirement := range requirements {
		if schemes, ok := requirement.(map[string]any); ok {
			if _, ok := schemes["bearerAuth"]; ok {
				return true
			}
		}
	}
	return false
}

func serverTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("GRAPHNEST_TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("GRAPHNEST_REQUIRE_POSTGRES") == "1" {
			t.Fatal("GRAPHNEST_TEST_POSTGRES_DSN is not set")
		}
		t.Skip("GRAPHNEST_TEST_POSTGRES_DSN is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{"graphnest_server_" + rand.Text()}.Sanitize()
	if _, err := admin.Exec(t.Context(), "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "drop schema "+schema+" cascade"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
