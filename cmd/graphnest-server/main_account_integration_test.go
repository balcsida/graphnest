//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
