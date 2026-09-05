//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/oauthas"
	"github.com/jackc/pgx/v5"
)

func oauthConsent(t *testing.T, store *Store) (*oauthas.Server, *http.Request, int64, [32]byte, [32]byte) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "consent-owner", "consent-owner")
	client := seedOAuthClient(t, store, now)
	sessions := &authn.SessionManager{Store: store, IdleTTL: time.Minute, TTL: time.Hour, Now: func() time.Time { return now }}
	token, _, err := sessions.CreateForUser(t.Context(), userID, authn.ProviderOIDC, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	handleBytes := []byte(strings.Repeat("h", 32))
	handle := base64.RawURLEncoding.EncodeToString(handleBytes)
	pendingID := sha256.Sum256(handleBytes)
	requestID := hex.EncodeToString(pendingID[:])
	if err := store.CreateOAuthAuthorizationRequest(t.Context(), authn.OAuthAuthorizationRequest{
		ID: pendingID, Phase: "pending", ClientID: client.ID, RedirectURI: client.RedirectURIs[0],
		CodeChallenge: strings.Repeat("a", 43), CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	server := &oauthas.Server{Store: store, Sessions: sessions, Origin: "https://graphnest.example", Now: sessions.Now}
	request := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(url.Values{
		"request_id": {requestID}, "decision": {"allow"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", server.Origin)
	request.AddCookie(&http.Cookie{Name: oauthas.RequestCookie + "_" + requestID, Value: handle})
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: token})
	return server, request, userID, sha256.Sum256(raw), pendingID
}

func TestOAuthConsentRejectsRevocationAfterAuthentication(t *testing.T) {
	store := migratedStore(t)
	server, request, userID, _, _ := oauthConsent(t, store)
	server.Rand = &revokeBeforeTokenGeneration{Reader: rand.Reader, revoke: func() {
		if err := store.RevokeAdminUserCredentials(t.Context(), userID); err != nil {
			t.Fatal(err)
		}
	}}
	mux := http.NewServeMux()
	server.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var active bool
	var codes, grants int
	if err := store.pool.QueryRow(t.Context(), `select scim_active and suspended_at is null and deleted_at is null,
		(select count(*) from oauth_authorization_requests where phase='code'),
		(select count(*) from oauth_grants) from users where id=$1`, userID).Scan(&active, &codes, &grants); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusSeeOther || location.Query().Get("code") != "" || location.Query().Get("error") != "server_error" || !active || codes != 0 || grants != 0 {
		t.Fatalf("status=%d location=%s active=%t codes=%d grants=%d", response.Code, location, active, codes, grants)
	}
}

func TestOAuthConsentRequiresEligibleSession(t *testing.T) {
	for _, test := range []struct {
		name, update string
		valid        bool
	}{
		{"oidc", "", true},
		{"oauth", `update auth_sessions set provider='oauth'`, true},
		{"local", `update users set source='local'; insert into user_roles(user_id,administrator) select id,true from users; update auth_sessions set provider='local'`, true},
		{"missing session", `delete from auth_sessions`, false},
		{"wrong session hash", "", false},
		{"revoked", `update auth_sessions set revoked_at=now()`, false},
		{"forced rotation", `update auth_sessions set force_rotation=true`, false},
		{"local without role", `update users set source='local'; update auth_sessions set provider='local'`, false},
		{"local nonlocal user", `insert into user_roles(user_id,administrator) select id,true from users; update auth_sessions set provider='local'`, false},
		{"inactive", `update users set scim_active=false`, false},
		{"suspended", `update users set suspended_at=now()`, false},
		{"deleted", `update users set deleted_at=now()`, false},
		{"absolute expiry", "", false},
		{"idle expiry", "", false},
		{"other owner", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := migratedStore(t)
			server, _, userID, sessionHash, pendingID := oauthConsent(t, store)
			if test.update != "" {
				if _, err := store.pool.Exec(t.Context(), test.update); err != nil {
					t.Fatal(err)
				}
			}
			switch test.name {
			case "wrong session hash":
				sessionHash = [32]byte{99}
			case "absolute expiry", "idle expiry":
				expiresAt := server.Now().Add(time.Hour)
				if test.name == "absolute expiry" {
					expiresAt = server.Now()
				}
				if _, err := store.pool.Exec(t.Context(), `update auth_sessions set created_at=$1::timestamptz-interval '1 hour',
					last_seen_at=$1::timestamptz-interval '1 hour',idle_expires_at=$1,expires_at=$2`, server.Now(), expiresAt); err != nil {
					t.Fatal(err)
				}
			case "other owner":
				other := insertIdentityUser(t, store, "other-owner", "other-owner")
				if _, err := store.pool.Exec(t.Context(), `update auth_sessions set user_id=$1`, other); err != nil {
					t.Fatal(err)
				}
			}
			codeID := [32]byte{81}
			err := store.IssueOAuthCode(t.Context(), pendingID, codeID, sessionHash, userID, server.Now().Add(time.Minute), server.Now())
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("ineligible session issued code: %v", err)
				}
				if _, err := store.OAuthAuthorizationRequest(t.Context(), pendingID, "pending", server.Now()); err != nil {
					t.Fatalf("rejected consent consumed pending request: %v", err)
				}
			}
		})
	}
}

func TestOAuthConsentSerializesWithRevocation(t *testing.T) {
	store := migratedStore(t)
	server, _, userID, sessionHash, pendingID := oauthConsent(t, store)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	blocker, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	var blockerPID int
	if err := blocker.QueryRow(ctx, `select pg_backend_pid() from pg_advisory_xact_lock(629005)`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `create function pause_oauth_consent() returns trigger language plpgsql as $$
		begin perform pg_advisory_xact_lock(629005); return new; end $$;
		create trigger pause_oauth_consent before update on oauth_authorization_requests for each row execute function pause_oauth_consent()`); err != nil {
		t.Fatal(err)
	}
	codeID := [32]byte{82}
	issued := make(chan error, 1)
	go func() {
		issued <- store.IssueOAuthCode(ctx, pendingID, codeID, sessionHash, userID, server.Now().Add(time.Minute), server.Now())
	}()
	issuancePID := waitOAuthLock(t, ctx, store, blockerPID, issued)
	revoked := make(chan error, 1)
	go func() { revoked <- store.RevokeAdminUserCredentials(ctx, userID) }()
	waitOAuthLock(t, ctx, store, issuancePID, revoked)
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-issued; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if _, err := store.OAuthAuthorizationRequest(ctx, codeID, "code", server.Now()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revocation missed code issued concurrently: %v", err)
	}
}

func TestOAuthConsentWaitsForRevocation(t *testing.T) {
	for _, logout := range []bool{false, true} {
		name := "credentials"
		if logout {
			name = "logout"
		}
		t.Run(name, func(t *testing.T) {
			store := migratedStore(t)
			server, _, userID, sessionHash, pendingID := oauthConsent(t, store)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			revocation, err := store.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer revocation.Rollback(context.Background())
			if logout {
				_, err = revocation.Exec(ctx, `update auth_sessions set revoked_at=now() where token_hash=$1`, sessionHash[:])
			} else {
				if err := requireAdminUser(ctx, revocation, userID); err != nil {
					t.Fatal(err)
				}
				err = revokeAdminCredentials(ctx, revocation, userID)
			}
			if err != nil {
				t.Fatal(err)
			}
			var revocationPID int
			if err := revocation.QueryRow(ctx, `select pg_backend_pid()`).Scan(&revocationPID); err != nil {
				t.Fatal(err)
			}
			issued := make(chan error, 1)
			go func() {
				issued <- store.IssueOAuthCode(ctx, pendingID, [32]byte{83}, sessionHash, userID, server.Now().Add(time.Minute), server.Now())
			}()
			waitOAuthLock(t, ctx, store, revocationPID, issued)
			if err := revocation.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-issued; !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("consent accepted revoked session: %v", err)
			}
		})
	}
}
