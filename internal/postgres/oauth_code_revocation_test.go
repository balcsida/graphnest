//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/oauthas"
	"github.com/jackc/pgx/v5"
)

type revokeBeforeTokenGeneration struct {
	io.Reader
	revoke            func()
	readsBeforeRevoke int
}

func (r *revokeBeforeTokenGeneration) Read(p []byte) (int, error) {
	if r.revoke != nil && r.readsBeforeRevoke == 0 {
		r.revoke()
		r.revoke = nil
	}
	r.readsBeforeRevoke--
	return r.Reader.Read(p)
}

func oauthCodeExchange(t *testing.T, store *Store) (*oauthas.Server, *http.Request, int64) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "code-owner", "code-owner")
	client := seedOAuthClient(t, store, now)
	verifier := strings.Repeat("v", 43)
	challenge := sha256.Sum256([]byte(verifier))
	pending := authn.OAuthAuthorizationRequest{
		ID: [32]byte{10}, Phase: "pending", ClientID: client.ID, RedirectURI: client.RedirectURIs[0],
		CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]),
		CreatedAt:     now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := store.CreateOAuthAuthorizationRequest(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	codeBytes := []byte(strings.Repeat("c", 32))
	codeHash := sha256.Sum256(codeBytes)
	sessionHash := [32]byte{91}
	if err := store.CreateSession(t.Context(), authn.SessionRecord{TokenHash: sessionHash, UserID: userID, Provider: authn.ProviderOIDC,
		CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.IssueOAuthCode(t.Context(), pending.ID, codeHash, sessionHash, userID, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	server := &oauthas.Server{Store: store, Limiter: store, Now: func() time.Time { return now }}
	request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(url.Values{
		"grant_type": {"authorization_code"}, "code": {oauthas.CodePrefix + base64.RawURLEncoding.EncodeToString(codeBytes)},
		"client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]}, "code_verifier": {verifier},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = "192.0.2.1:12345"
	return server, request, userID
}

func TestOAuthCodeExchangeRejectsRevokedCredentials(t *testing.T) {
	for _, during := range []bool{false, true} {
		name := "before exchange"
		if during {
			name = "after validation before grant creation"
		}
		t.Run(name, func(t *testing.T) {
			store := migratedStore(t)
			server, request, userID := oauthCodeExchange(t, store)
			revoke := func() {
				if err := store.RevokeAdminUserCredentials(t.Context(), userID); err != nil {
					t.Fatal(err)
				}
			}
			if during {
				server.Rand = &revokeBeforeTokenGeneration{Reader: rand.Reader, revoke: revoke}
			} else {
				revoke()
			}
			mux := http.NewServeMux()
			server.Register(mux)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			var result struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var grants, codes int
			if err := store.pool.QueryRow(t.Context(), `select
				(select count(*) from oauth_grants where user_id=$1 and revoked_at is null),
				(select count(*) from oauth_authorization_requests where user_id=$1)`, userID).Scan(&grants, &codes); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusBadRequest || result.Error != "invalid_grant" || grants != 0 || codes != 0 {
				t.Fatalf("status=%d error=%q live grants=%d codes=%d", response.Code, result.Error, grants, codes)
			}
		})
	}
}

func TestOAuthCodeExchangeSerializesWithRevocation(t *testing.T) {
	store := migratedStore(t)
	server, request, userID := oauthCodeExchange(t, store)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	blocker, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	var blockerPID int
	if err := blocker.QueryRow(ctx, `select pg_backend_pid() from pg_advisory_xact_lock(629004)`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	// Pause inside grant insertion, after code consumption, without timing sleeps.
	if _, err := store.pool.Exec(ctx, `create function pause_oauth_grant() returns trigger language plpgsql as $$
		begin perform pg_advisory_xact_lock(629004); return new; end $$;
		create trigger pause_oauth_grant before insert on oauth_grants for each row execute function pause_oauth_grant()`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server.Register(mux)
	response := httptest.NewRecorder()
	exchanged := make(chan struct{})
	go func() {
		defer close(exchanged)
		mux.ServeHTTP(response, request.WithContext(ctx))
	}()
	exchangePID := waitOAuthLock(t, ctx, store, blockerPID, nil)
	revoked := make(chan error, 1)
	go func() { revoked <- store.RevokeAdminUserCredentials(ctx, userID) }()
	// Revocation must wait on the exchange's user lock, not miss its new grant.
	waitOAuthLock(t, ctx, store, exchangePID, revoked)
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exchanged:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if response.Code != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	token, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(result.AccessToken, oauthas.AccessTokenPrefix))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OAuthPrincipal(ctx, sha256.Sum256(token), server.Now()); err == nil {
		t.Fatal("revocation missed the concurrently created grant")
	}
}

func TestOAuthCodeExchangeWaitsForRevocation(t *testing.T) {
	store := migratedStore(t)
	server, request, userID := oauthCodeExchange(t, store)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	revocation, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer revocation.Rollback(context.Background())
	if err := requireAdminUser(ctx, revocation, userID); err != nil {
		t.Fatal(err)
	}
	if err := revokeAdminCredentials(ctx, revocation, userID); err != nil {
		t.Fatal(err)
	}
	var revocationPID int
	if err := revocation.QueryRow(ctx, `select pg_backend_pid()`).Scan(&revocationPID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server.Register(mux)
	response := httptest.NewRecorder()
	exchanged := make(chan struct{})
	go func() {
		defer close(exchanged)
		mux.ServeHTTP(response, request.WithContext(ctx))
	}()
	// The handler read the still-visible code but must wait for the user lock.
	waitOAuthLock(t, ctx, store, revocationPID, nil)
	if err := revocation.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exchanged:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"invalid_grant"`) {
		t.Fatalf("exchange status=%d body=%s", response.Code, response.Body.String())
	}
	var grants int
	if err := store.pool.QueryRow(ctx, `select count(*) from oauth_grants where user_id=$1`, userID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("grants=%d err=%v", grants, err)
	}
}

func TestOAuthCodeExchangeRollsBackFailedGrant(t *testing.T) {
	store := migratedStore(t)
	server, _, userID := oauthCodeExchange(t, store)
	codeHash := sha256.Sum256([]byte(strings.Repeat("c", 32)))
	code, err := store.OAuthAuthorizationRequest(t.Context(), codeHash, "code", server.Now())
	if err != nil {
		t.Fatal(err)
	}
	grant := authn.OAuthGrant{UserID: userID, ClientID: code.ClientID, CreatedAt: server.Now(), ExpiresAt: server.Now()}
	if _, err := store.ExchangeOAuthCode(t.Context(), codeHash, grant); err == nil || errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected grant lifetime constraint failure, got %v", err)
	}
	grant.ExpiresAt = server.Now().Add(time.Hour)
	if _, err := store.ExchangeOAuthCode(t.Context(), codeHash, grant); err != nil {
		t.Fatalf("failed grant insertion consumed code: %v", err)
	}
}

type unusedOAuthGitHubAccess struct{}

type failOAuthCodeExchangeOnce struct {
	authn.OAuthStore
	failed bool
}

func (s *failOAuthCodeExchangeOnce) ExchangeOAuthCode(ctx context.Context, codeID [32]byte, grant authn.OAuthGrant) (int64, error) {
	if !s.failed {
		s.failed = true
		return 0, errors.New("injected OAuth code exchange failure")
	}
	return s.OAuthStore.ExchangeOAuthCode(ctx, codeID, grant)
}

type pauseFirstOAuthCodeExchange struct {
	authn.OAuthStore
	once    sync.Once
	entered chan struct{}
	release <-chan struct{}
}

func (s *pauseFirstOAuthCodeExchange) ExchangeOAuthCode(ctx context.Context, codeID [32]byte, grant authn.OAuthGrant) (int64, error) {
	pause := false
	s.once.Do(func() {
		pause = true
		close(s.entered)
	})
	if pause {
		select {
		case <-s.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return s.OAuthStore.ExchangeOAuthCode(ctx, codeID, grant)
}

func (unusedOAuthGitHubAccess) AccessibleRepositories(context.Context, string) ([]int64, error) {
	return nil, nil
}

func githubOAuthCodeExchange(t *testing.T, store *Store, now func() time.Time) (*oauthas.Server, *http.Request, int64) {
	t.Helper()
	server, request, userID := oauthCodeExchange(t, store)
	var err error
	server.Sealer, err = oauthas.NewSealer(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	tokens := oauthas.NewProviderTokens(now)
	session := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	tokens.StoreProviderToken(t.Context(), session, "test-provider-token")
	tokens.Transfer(session, sha256.Sum256([]byte(strings.Repeat("c", 32))))
	server.GitHubTokens, server.GitHub = tokens, unusedOAuthGitHubAccess{}
	return server, request, userID
}

func copyOAuthCodeExchangeRequest(t *testing.T, original *http.Request) *http.Request {
	t.Helper()
	if err := original.ParseForm(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(t.Context(), original.Method, original.URL.String(), strings.NewReader(original.PostForm.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = original.RemoteAddr
	return request
}

func oauthError(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var result struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Error
}

func TestOAuthCodeExchangeRetainsGitHubHandoffAfterRandomFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		random int
	}{
		{name: "access token", random: 0},
		{name: "refresh token", random: 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := migratedStore(t)
			server, request, userID := githubOAuthCodeExchange(t, store, nil)
			server.Rand = bytes.NewReader(make([]byte, tc.random))
			mux := http.NewServeMux()
			server.Register(mux)

			failed := httptest.NewRecorder()
			mux.ServeHTTP(failed, copyOAuthCodeExchangeRequest(t, request))
			if failed.Code != http.StatusInternalServerError || oauthError(t, failed) != "server_error" {
				t.Fatalf("failed exchange status=%d body=%s", failed.Code, failed.Body.String())
			}

			server.Rand = rand.Reader
			retried := httptest.NewRecorder()
			mux.ServeHTTP(retried, copyOAuthCodeExchangeRequest(t, request))
			var grants int
			if err := store.pool.QueryRow(t.Context(), `select count(*) from oauth_grants where user_id=$1 and revoked_at is null`, userID).Scan(&grants); err != nil {
				t.Fatal(err)
			}
			if retried.Code != http.StatusOK || grants != 1 {
				t.Fatalf("retry status=%d body=%s live grants=%d", retried.Code, retried.Body.String(), grants)
			}
		})
	}
}

func TestOAuthCodeExchangeRetainsGitHubHandoffAfterStoreFailure(t *testing.T) {
	store := migratedStore(t)
	server, request, userID := githubOAuthCodeExchange(t, store, nil)
	server.Store = &failOAuthCodeExchangeOnce{OAuthStore: store}
	mux := http.NewServeMux()
	server.Register(mux)

	failed := httptest.NewRecorder()
	mux.ServeHTTP(failed, copyOAuthCodeExchangeRequest(t, request))
	if failed.Code != http.StatusServiceUnavailable || oauthError(t, failed) != "temporarily_unavailable" {
		t.Fatalf("failed exchange status=%d body=%s", failed.Code, failed.Body.String())
	}

	retried := httptest.NewRecorder()
	mux.ServeHTTP(retried, copyOAuthCodeExchangeRequest(t, request))
	var grants int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from oauth_grants where user_id=$1 and revoked_at is null`, userID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if retried.Code != http.StatusOK || grants != 1 {
		t.Fatalf("retry status=%d body=%s live grants=%d", retried.Code, retried.Body.String(), grants)
	}
}

func TestOAuthCodeExchangeConcurrentRequestsIssueOneGitHubGrant(t *testing.T) {
	store := migratedStore(t)
	server, request, userID := githubOAuthCodeExchange(t, store, nil)
	release := make(chan struct{})
	paused := &pauseFirstOAuthCodeExchange{OAuthStore: store, entered: make(chan struct{}), release: release}
	server.Store = paused
	mux := http.NewServeMux()
	server.Register(mux)

	first, second := httptest.NewRecorder(), httptest.NewRecorder()
	firstRequest := copyOAuthCodeExchangeRequest(t, request)
	secondRequest := copyOAuthCodeExchangeRequest(t, request)
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go func() {
		mux.ServeHTTP(first, firstRequest)
		close(firstDone)
	}()
	select {
	case <-paused.entered:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	go func() {
		mux.ServeHTTP(second, secondRequest)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	close(release)
	select {
	case <-firstDone:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}

	var grants int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from oauth_grants where user_id=$1 and revoked_at is null`, userID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusBadRequest || oauthError(t, first) != "invalid_grant" || second.Code != http.StatusOK || grants != 1 {
		t.Fatalf("first status=%d body=%s second status=%d body=%s live grants=%d", first.Code, first.Body.String(), second.Code, second.Body.String(), grants)
	}
}

func TestOAuthCodeExchangeRetryDoesNotExtendGitHubHandoff(t *testing.T) {
	store := migratedStore(t)
	handoffNow := time.Now()
	handoffStarted := handoffNow
	server, request, userID := githubOAuthCodeExchange(t, store, func() time.Time { return handoffNow })
	mux := http.NewServeMux()
	server.Register(mux)

	for _, elapsed := range []time.Duration{30 * time.Second, 40 * time.Second} {
		handoffNow = handoffStarted.Add(elapsed)
		server.Rand = bytes.NewReader(nil)
		failed := httptest.NewRecorder()
		mux.ServeHTTP(failed, copyOAuthCodeExchangeRequest(t, request))
		if failed.Code != http.StatusInternalServerError || oauthError(t, failed) != "server_error" {
			t.Fatalf("after %s status=%d body=%s", elapsed, failed.Code, failed.Body.String())
		}
	}
	handoffNow = handoffStarted.Add(61 * time.Second)
	server.Rand = rand.Reader
	expired := httptest.NewRecorder()
	mux.ServeHTTP(expired, copyOAuthCodeExchangeRequest(t, request))
	var grants int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from oauth_grants where user_id=$1 and revoked_at is null`, userID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if expired.Code != http.StatusBadRequest || oauthError(t, expired) != "invalid_grant" || grants != 0 {
		t.Fatalf("expired retry status=%d body=%s live grants=%d", expired.Code, expired.Body.String(), grants)
	}
}

func TestOAuthCodeExchangeCannotRestoreRevokedGitHubToken(t *testing.T) {
	store := migratedStore(t)
	server, request, userID := oauthCodeExchange(t, store)
	var err error
	server.Sealer, err = oauthas.NewSealer(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	tokens := oauthas.NewProviderTokens(server.Now)
	session := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	tokens.StoreProviderToken(t.Context(), session, "test-provider-token")
	codeHash := sha256.Sum256([]byte(strings.Repeat("c", 32)))
	tokens.Transfer(session, codeHash)
	server.GitHubTokens, server.GitHub = tokens, unusedOAuthGitHubAccess{}
	server.Rand = &revokeBeforeTokenGeneration{Reader: rand.Reader, readsBeforeRevoke: 2, revoke: func() {
		// The third read seals the provider token after the exchange has committed.
		if err := store.RevokeAdminUserCredentials(t.Context(), userID); err != nil {
			t.Fatal(err)
		}
	}}
	mux := http.NewServeMux()
	server.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var retained, active bool
	if err := store.pool.QueryRow(t.Context(), `select github_token_ct is not null, revoked_at is null
		from oauth_grants where user_id=$1`, userID).Scan(&retained, &active); err != nil {
		t.Fatalf("status=%d body=%s: %v", response.Code, response.Body.String(), err)
	}
	if response.Code != http.StatusServiceUnavailable || retained || active {
		t.Fatalf("status=%d retained provider token=%t active=%t", response.Code, retained, active)
	}
	if _, ok := tokens.TokenForCode(codeHash); ok {
		t.Fatal("provider token remained after committed exchange failed to store it")
	}
}

func waitOAuthLock(t *testing.T, ctx context.Context, store *Store, blockerPID int, completed <-chan error) int {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		if err := store.pool.QueryRow(ctx, `select coalesce(min(pid), 0) from pg_stat_activity
			where $1=any(pg_blocking_pids(pid))`, blockerPID).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case err := <-completed:
			t.Fatalf("revocation completed before code exchange committed: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}
