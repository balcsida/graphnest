//go:build integration

package postgres

import (
	"slices"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

func TestOAuthGrantsUseOwnerScopedKeysetPages(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	ownerID := insertIdentityUser(t, store, "pagination-owner", "owner")
	otherID := insertIdentityUser(t, store, "pagination-other", "other")
	client := seedOAuthClient(t, store, now)

	var ownerGrantIDs []int64
	create := func(userID int64, value byte) int64 {
		t.Helper()
		id, err := store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
			ClientID: client.ID, UserID: userID, AccessHash: [32]byte{value}, AccessExpiresAt: now.Add(time.Hour),
			RefreshHash: [32]byte{value + 20}, CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for value := byte(1); value <= 5; value++ {
		ownerGrantIDs = append(ownerGrantIDs, create(ownerID, value))
		if value < 5 {
			create(otherID, value+10)
		}
	}

	var got []int64
	afterID := int64(0)
	for pageNumber := 1; ; pageNumber++ {
		page, truncated, err := store.ListOAuthGrants(t.Context(), ownerID, afterID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			t.Fatalf("page %d is empty before terminal page", pageNumber)
		}
		for _, grant := range page {
			if grant.ID <= afterID {
				t.Fatalf("page %d contains non-increasing grant %d after %d", pageNumber, grant.ID, afterID)
			}
			got = append(got, grant.ID)
			afterID = grant.ID
		}
		if !truncated {
			if pageNumber != 3 {
				t.Fatalf("terminal page=%d want 3", pageNumber)
			}
			break
		}
	}
	if !slices.Equal(got, ownerGrantIDs) {
		t.Fatalf("listed grant IDs=%v want owner IDs=%v", got, ownerGrantIDs)
	}
}
