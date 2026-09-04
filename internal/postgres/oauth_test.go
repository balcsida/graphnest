//go:build integration

package postgres

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

func seedOAuthClient(t *testing.T, store *Store, now time.Time) authn.OAuthClient {
	t.Helper()
	client := authn.OAuthClient{ID: "gnc_test-client", Name: "OpenCode", RedirectURIs: []string{"http://127.0.0.1:19876/mcp/oauth/callback"}, CreatedAt: now}
	if err := store.CreateOAuthClient(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestOAuthAuthorizationRequestBecomesSingleUseCode(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "directory-1", "ada")
	client := seedOAuthClient(t, store, now)

	pending := authn.OAuthAuthorizationRequest{
		ID: [32]byte{1}, Phase: "pending", ClientID: client.ID, RedirectURI: client.RedirectURIs[0],
		CodeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", State: "xyz", Resource: "https://graphnest.example/mcp",
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := store.CreateOAuthAuthorizationRequest(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.OAuthAuthorizationRequest(t.Context(), pending.ID, "pending", now)
	if err != nil || loaded.ClientID != client.ID || loaded.UserID != 0 || loaded.State != "xyz" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if _, err := store.OAuthAuthorizationRequest(t.Context(), pending.ID, "code", now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("pending request must not be loadable as a code: %v", err)
	}
	if _, err := store.OAuthAuthorizationRequest(t.Context(), pending.ID, "pending", now.Add(11*time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired request must be invisible: %v", err)
	}

	code := [32]byte{2}
	if err := store.IssueOAuthCode(t.Context(), pending.ID, code, userID, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := store.IssueOAuthCode(t.Context(), pending.ID, [32]byte{3}, userID, now.Add(time.Minute), now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("request handle must stop working once consent is given: %v", err)
	}
	consumed, err := store.ConsumeOAuthCode(t.Context(), code, now.Add(30*time.Second))
	if err != nil || consumed.UserID != userID || consumed.Phase != "code" || consumed.CodeChallenge != pending.CodeChallenge {
		t.Fatalf("consumed=%+v err=%v", consumed, err)
	}
	if _, err := store.ConsumeOAuthCode(t.Context(), code, now.Add(30*time.Second)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("code must be single use: %v", err)
	}
}

func TestOAuthCodeExpiryAndCleanup(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "directory-2", "bob")
	client := seedOAuthClient(t, store, now)
	code := authn.OAuthAuthorizationRequest{
		ID: [32]byte{9}, Phase: "code", ClientID: client.ID, UserID: userID, RedirectURI: client.RedirectURIs[0],
		CodeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if err := store.CreateOAuthAuthorizationRequest(t.Context(), code); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeOAuthCode(t.Context(), code.ID, now.Add(2*time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired code must not be exchangeable: %v", err)
	}
	requests, _, _, err := store.DeleteExpiredOAuth(t.Context(), now.Add(2*time.Minute))
	if err != nil || requests != 1 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestOAuthGrantAuthenticatesRotatesAndDetectsReplay(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "directory-3", "cy")
	client := seedOAuthClient(t, store, now)

	grantID, err := store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		ClientID: client.ID, UserID: userID, AccessHash: [32]byte{10}, AccessExpiresAt: now.Add(time.Hour),
		RefreshHash: [32]byte{11}, GitHubTokenCiphertext: []byte("ct"), CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	if err != nil || grantID == 0 {
		t.Fatalf("grantID=%d err=%v", grantID, err)
	}

	principal, err := store.OAuthPrincipal(t.Context(), [32]byte{10}, now.Add(time.Minute))
	if err != nil || principal.Subject != strconv.FormatInt(userID, 10) || principal.Method != authn.ProviderOAuthToken {
		t.Fatalf("principal=%+v err=%v", principal, err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{10}, now.Add(2*time.Hour)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired access token must not authenticate: %v", err)
	}

	rotated, err := store.RotateOAuthGrant(t.Context(), [32]byte{11}, authn.OAuthRotation{
		AccessHash: [32]byte{20}, AccessExpiresAt: now.Add(3 * time.Hour), RefreshHash: [32]byte{21}, Now: now.Add(2 * time.Hour),
	})
	if err != nil || rotated.ID != grantID || rotated.PreviousRefreshHash == nil || *rotated.PreviousRefreshHash != [32]byte{11} || string(rotated.GitHubTokenCiphertext) != "ct" {
		t.Fatalf("rotated=%+v err=%v", rotated, err)
	}
	if !rotated.ExpiresAt.Equal(now.Add(30 * 24 * time.Hour)) {
		t.Fatalf("absolute expiry moved: %v", rotated.ExpiresAt)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{20}, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("new access token must authenticate: %v", err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{10}, now.Add(2*time.Hour)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old access token must be dead after rotation: %v", err)
	}

	// Within the grace window the rotated token fails without revoking.
	if _, err := store.RotateOAuthGrant(t.Context(), [32]byte{11}, authn.OAuthRotation{AccessHash: [32]byte{30}, AccessExpiresAt: now.Add(4 * time.Hour), RefreshHash: [32]byte{31}, Now: now.Add(2*time.Hour + 10*time.Second), Grace: 30 * time.Second}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retry within grace err=%v, want ErrNoRows", err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{20}, now.Add(2*time.Hour+10*time.Second)); err != nil {
		t.Fatalf("grant must survive a retry within grace: %v", err)
	}
	// Replaying the rotated refresh token later revokes the whole grant.
	if _, err := store.RotateOAuthGrant(t.Context(), [32]byte{11}, authn.OAuthRotation{AccessHash: [32]byte{30}, AccessExpiresAt: now.Add(4 * time.Hour), RefreshHash: [32]byte{31}, Now: now.Add(3 * time.Hour), Grace: 30 * time.Second}); !errors.Is(err, authn.ErrOAuthReplay) {
		t.Fatalf("replay err=%v, want ErrOAuthReplay", err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{20}, now.Add(3*time.Hour)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("access token must be dead after replay revocation: %v", err)
	}
	if _, err := store.RotateOAuthGrant(t.Context(), [32]byte{21}, authn.OAuthRotation{AccessHash: [32]byte{40}, AccessExpiresAt: now.Add(4 * time.Hour), RefreshHash: [32]byte{41}, Now: now.Add(3 * time.Hour)}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("current refresh token must be dead after replay revocation: %v", err)
	}
	var ciphertext []byte
	if err := store.pool.QueryRow(t.Context(), `select github_token_ct from oauth_grants where id=$1`, grantID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	// Replay revocation is store-internal; the ciphertext is cleared by the
	// explicit revoke paths, which the authorization server calls next.
	if err := store.RevokeOAuthGrant(t.Context(), grantID); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `select github_token_ct from oauth_grants where id=$1`, grantID).Scan(&ciphertext); err != nil || ciphertext != nil {
		t.Fatalf("ciphertext=%q err=%v, want cleared", ciphertext, err)
	}
	if _, err := store.RotateOAuthGrant(t.Context(), [32]byte{99}, authn.OAuthRotation{Now: now}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown refresh token err=%v", err)
	}
}

func TestOAuthGrantsAreListedRevokedAndSwept(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "directory-4", "dee")
	other := insertIdentityUser(t, store, "directory-5", "eve")
	client := seedOAuthClient(t, store, now)
	grantID, err := store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		ClientID: client.ID, UserID: userID, AccessHash: [32]byte{50}, AccessExpiresAt: now.Add(time.Hour),
		RefreshHash: [32]byte{51}, CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := store.ListOAuthGrants(t.Context(), userID)
	if err != nil || len(grants) != 1 || grants[0].ID != grantID || grants[0].ClientName != "OpenCode" {
		t.Fatalf("grants=%+v err=%v", grants, err)
	}
	if err := store.RevokeUserOAuthGrant(t.Context(), other, grantID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another user must not revoke the grant: %v", err)
	}
	if err := store.RevokeOAuthGrantByToken(t.Context(), [32]byte{51}, "gnc_someone-else"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{50}, now); err != nil {
		t.Fatalf("another client's revoke must not affect the grant: %v", err)
	}
	if err := store.RevokeOAuthGrantByToken(t.Context(), [32]byte{51}, client.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{50}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked grant must not authenticate: %v", err)
	}
	if grants, err := store.ListOAuthGrants(t.Context(), userID); err != nil || len(grants) != 0 {
		t.Fatalf("revoked grants must disappear from the account list: %+v err=%v", grants, err)
	}
	if _, grantsDeleted, _, err := store.DeleteExpiredOAuth(t.Context(), now.Add(6*24*time.Hour)); err != nil || grantsDeleted != 0 {
		t.Fatalf("grants deleted too early: %d err=%v", grantsDeleted, err)
	}
	_, grantsDeleted, clients, err := store.DeleteExpiredOAuth(t.Context(), now.Add(91*24*time.Hour))
	if err != nil || grantsDeleted != 1 || clients != 1 {
		t.Fatalf("grants=%d clients=%d err=%v", grantsDeleted, clients, err)
	}
}

func TestOAuthPrincipalRequiresLiveUser(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "directory-6", "fay")
	client := seedOAuthClient(t, store, now)
	if _, err := store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		ClientID: client.ID, UserID: userID, AccessHash: [32]byte{60}, AccessExpiresAt: now.Add(time.Hour),
		RefreshHash: [32]byte{61}, CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update users set suspended_at=now() where id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{60}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("suspended user's token must not authenticate: %v", err)
	}
	if _, err := store.OAuthGrantByRefresh(t.Context(), [32]byte{61}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("suspended user's refresh token must not resolve: %v", err)
	}
}

func TestReplaceGitHubGrantsDropsUnknownRepositories(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-7", "gus")
	if err := store.ReplaceGitHubGrants(t.Context(), userID, []int64{404}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from user_github_grants where user_id=$1`, userID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestLoginFlowReturnToAcceptsOAuthResume(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.CreateLoginFlow(t.Context(), authn.LoginFlow{
		StateHash: [32]byte{70}, BrowserHash: [32]byte{71}, Provider: "github", Nonce: "n", CodeVerifier: "v",
		ReturnTo: "/oauth/authorize/resume", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateLoginFlow(t.Context(), authn.LoginFlow{
		StateHash: [32]byte{72}, BrowserHash: [32]byte{73}, Provider: "github", Nonce: "n", CodeVerifier: "v",
		ReturnTo: "/evil", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}); err == nil {
		t.Fatal("arbitrary return_to must be rejected by the schema")
	}
}

func TestUserDisplayNameForConsent(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-8", "hal")
	if name, err := store.UserDisplayName(t.Context(), userID); err != nil || name != "hal" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	if _, err := store.pool.Exec(t.Context(), `update users set display_name='Hal Abelson' where id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if name, err := store.UserDisplayName(t.Context(), userID); err != nil || name != "Hal Abelson (hal)" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	if _, err := store.UserDisplayName(t.Context(), 404); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown user err=%v", err)
	}
}

func TestAdminCredentialRevocationCoversOAuthGrants(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "directory-9", "ivy")
	client := seedOAuthClient(t, store, now)
	if _, err := store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		ClientID: client.ID, UserID: userID, AccessHash: [32]byte{80}, AccessExpiresAt: now.Add(time.Hour),
		RefreshHash: [32]byte{81}, GitHubTokenCiphertext: []byte("ct"), CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := revokeAdminCredentials(t.Context(), tx, userID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{80}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("grant must be revoked with the user's other credentials: %v", err)
	}
	var ciphertext []byte
	if err := store.pool.QueryRow(t.Context(), `select github_token_ct from oauth_grants where user_id=$1`, userID).Scan(&ciphertext); err != nil || ciphertext != nil {
		t.Fatalf("ciphertext=%q err=%v, want cleared", ciphertext, err)
	}
}
