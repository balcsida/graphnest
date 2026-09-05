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

func TestOAuthRefreshReplayRetainsEntireFamily(t *testing.T) {
	for _, rotations := range []int{1, 3} {
		t.Run(strconv.Itoa(rotations)+"_rotations", func(t *testing.T) {
			store, original := seedReplayGrant(t)
			firstRotation := original.CreatedAt.Add(time.Hour)
			current := original
			for i := range rotations {
				var err error
				current, err = store.RotateOAuthGrant(t.Context(), current.RefreshHash, authn.OAuthRotation{
					AccessHash: [32]byte{byte(20 + i)}, AccessExpiresAt: original.AccessExpiresAt,
					RefreshHash: [32]byte{byte(30 + i)}, Now: firstRotation.Add(time.Duration(i) * 10 * time.Second), Grace: 30 * time.Second,
					Audit: testOAuthRefreshAudit(original.UserID, original.ID),
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			// Even the oldest consumed token has its own lost-response grace.
			if _, err := store.RotateOAuthGrant(t.Context(), original.RefreshHash, authn.OAuthRotation{
				Now: firstRotation.Add(25 * time.Second), Grace: 30 * time.Second,
			}); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("within-grace replay=%v, want no rows", err)
			}
			// Access traffic and later rotations must not extend that deadline.
			if _, err := store.OAuthPrincipal(t.Context(), current.AccessHash, firstRotation.Add(31*time.Second)); err != nil {
				t.Fatalf("within-grace replay revoked grant: %v", err)
			}
			if _, err := store.RotateOAuthGrant(t.Context(), original.RefreshHash, authn.OAuthRotation{
				Now: firstRotation.Add(32 * time.Second), Grace: 30 * time.Second,
			}); !errors.Is(err, authn.ErrOAuthReplay) {
				t.Fatalf("outside-grace replay=%v, want OAuth replay", err)
			}
			if _, err := store.OAuthPrincipal(t.Context(), current.AccessHash, firstRotation.Add(32*time.Second)); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("replayed family access error=%v, want no rows", err)
			}
			if _, err := store.OAuthGrantByRefresh(t.Context(), current.RefreshHash, firstRotation.Add(32*time.Second)); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("replayed family refresh error=%v, want no rows", err)
			}
			var ciphertext []byte
			if err := store.pool.QueryRow(t.Context(), `select github_token_ct from oauth_grants where id=$1`, original.ID).Scan(&ciphertext); err != nil || ciphertext != nil {
				t.Fatalf("revoked grant ciphertext=%x err=%v", ciphertext, err)
			}
		})
	}
}

func TestRevokeOAuthGrantByConsumedRefresh(t *testing.T) {
	store, original := seedReplayGrant(t)
	current := original
	for i := range 3 {
		var err error
		current, err = store.RotateOAuthGrant(t.Context(), current.RefreshHash, authn.OAuthRotation{
			AccessHash: [32]byte{byte(20 + i)}, AccessExpiresAt: original.AccessExpiresAt,
			RefreshHash: [32]byte{byte(30 + i)}, Now: original.CreatedAt.Add(time.Duration(i+1) * time.Minute),
			Audit: testOAuthRefreshAudit(original.UserID, original.ID),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if revoked, err := store.RevokeOAuthGrantByToken(t.Context(), original.RefreshHash, original.ClientID); err != nil || !revoked {
		t.Fatalf("consumed refresh revocation: revoked=%v err=%v", revoked, err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), current.AccessHash, original.CreatedAt.Add(time.Hour)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("access after revocation by old refresh token=%v, want no rows", err)
	}
}

func TestOAuthRefreshHistoryMigrationRevokesLegacyRotations(t *testing.T) {
	pool := testPool(t)
	if err := migrateThrough(t.Context(), pool, 21); err != nil {
		t.Fatal(err)
	}
	store := New(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	client := seedOAuthClient(t, store, now)
	userID := insertIdentityUser(t, store, "legacy-oauth", "ada")
	for i := range 2 {
		grantID, err := store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
			ClientID: client.ID, UserID: userID, AccessHash: [32]byte{byte(10 + i)},
			AccessExpiresAt: now.Add(time.Hour), RefreshHash: [32]byte{byte(20 + i)},
			GitHubTokenCiphertext: []byte("ciphertext"), CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if _, err := pool.Exec(t.Context(), `update oauth_grants set previous_refresh_hash=$2 where id=$1`, grantID, bytes32(30)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{10}, now); err != nil {
		t.Fatalf("migration invalidated unrotated grant: %v", err)
	}
	if _, err := store.OAuthPrincipal(t.Context(), [32]byte{11}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("legacy rotated grant error=%v, want no rows", err)
	}
	var ciphertext []byte
	if err := pool.QueryRow(t.Context(), `select github_token_ct from oauth_grants where previous_refresh_hash is not null`).Scan(&ciphertext); err != nil || ciphertext != nil {
		t.Fatalf("legacy ciphertext=%x err=%v", ciphertext, err)
	}
}
