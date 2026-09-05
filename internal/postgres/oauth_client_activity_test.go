//go:build integration

package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

func TestOAuthClientLookupDoesNotRefreshActivity(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	client := seedOAuthClient(t, store, now.Add(-91*24*time.Hour))

	loaded, err := store.OAuthClient(t.Context(), client.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.LastUsedAt.Equal(client.CreatedAt) {
		t.Fatalf("last_used_at=%v want %v", loaded.LastUsedAt, client.CreatedAt)
	}

	_, _, clients, err := store.DeleteExpiredOAuth(t.Context(), now)
	if err != nil || clients != 1 {
		t.Fatalf("clients=%d err=%v", clients, err)
	}
	if _, err := store.OAuthClient(t.Context(), client.ID, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("idle client lookup error=%v want %v", err, pgx.ErrNoRows)
	}
}

func TestPendingOAuthAuthorizationRefreshesClientActivity(t *testing.T) {
	store := migratedStore(t)
	createdAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	client := seedOAuthClient(t, store, createdAt)
	authorizedAt := createdAt.Add(30 * time.Minute)
	request := authn.OAuthAuthorizationRequest{
		ID:            [32]byte{1},
		Phase:         "pending",
		ClientID:      client.ID,
		RedirectURI:   client.RedirectURIs[0],
		CodeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		CreatedAt:     authorizedAt,
		ExpiresAt:     authorizedAt.Add(10 * time.Minute),
	}
	if err := store.CreateOAuthAuthorizationRequest(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	if got := oauthClientLastUsedAt(t, store, client.ID); !got.Equal(authorizedAt) {
		t.Fatalf("last_used_at=%v want %v", got, authorizedAt)
	}

	request.UserID = insertIdentityUser(t, store, "directory-client-activity", "activity")
	request.ID = [32]byte{2}
	request.Phase = "code"
	request.CreatedAt = authorizedAt.Add(time.Minute)
	request.ExpiresAt = authorizedAt.Add(11 * time.Minute)
	if err := store.CreateOAuthAuthorizationRequest(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if got := oauthClientLastUsedAt(t, store, client.ID); !got.Equal(authorizedAt) {
		t.Fatalf("code insertion last_used_at=%v want %v", got, authorizedAt)
	}
}

func TestFailedOAuthAuthorizationDoesNotRefreshClientActivity(t *testing.T) {
	store := migratedStore(t)
	createdAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	client := seedOAuthClient(t, store, createdAt)
	request := authn.OAuthAuthorizationRequest{
		ID:            [32]byte{1},
		Phase:         "pending",
		ClientID:      client.ID,
		RedirectURI:   client.RedirectURIs[0],
		CodeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		CreatedAt:     createdAt.Add(10 * time.Minute),
		ExpiresAt:     createdAt.Add(20 * time.Minute),
	}
	if err := store.CreateOAuthAuthorizationRequest(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	request.CreatedAt = createdAt.Add(30 * time.Minute)
	request.ExpiresAt = createdAt.Add(40 * time.Minute)
	if err := store.CreateOAuthAuthorizationRequest(t.Context(), request); err == nil {
		t.Fatal("duplicate authorization request unexpectedly succeeded")
	}
	if got := oauthClientLastUsedAt(t, store, client.ID); !got.Equal(createdAt.Add(10 * time.Minute)) {
		t.Fatalf("last_used_at=%v want %v", got, createdAt.Add(10*time.Minute))
	}
}

func TestPendingOAuthAuthorizationRequiresClient(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	err := store.CreateOAuthAuthorizationRequest(t.Context(), authn.OAuthAuthorizationRequest{
		ID:            [32]byte{1},
		Phase:         "pending",
		ClientID:      "gnc_missing",
		RedirectURI:   "http://127.0.0.1:19876/mcp/oauth/callback",
		CodeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		CreatedAt:     now,
		ExpiresAt:     now.Add(10 * time.Minute),
	})
	if err == nil {
		t.Fatal("authorization request for missing client unexpectedly succeeded")
	}
}

func oauthClientLastUsedAt(t *testing.T, store *Store, clientID string) time.Time {
	t.Helper()
	var lastUsedAt time.Time
	if err := store.pool.QueryRow(t.Context(), `select last_used_at from oauth_clients where id=$1`, clientID).Scan(&lastUsedAt); err != nil {
		t.Fatal(err)
	}
	return lastUsedAt
}
