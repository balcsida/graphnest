//go:build integration

package postgres

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/admin"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/pkg/api"
)

func seedReplayGrant(t *testing.T) (*Store, authn.OAuthGrant) {
	t.Helper()
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	client := seedOAuthClient(t, store, now)
	grant := authn.OAuthGrant{
		ClientID: client.ID, UserID: insertIdentityUser(t, store, "oauth-replay", "ada"),
		AccessHash: [32]byte{10}, AccessExpiresAt: now.Add(2 * time.Hour),
		RefreshHash: [32]byte{11}, GitHubTokenCiphertext: []byte("ciphertext"),
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	var err error
	grant.ID, err = store.CreateOAuthGrant(t.Context(), grant)
	if err != nil {
		t.Fatal(err)
	}
	return store, grant
}

func TestOAuthAdministratorPrincipalIsReadOnly(t *testing.T) {
	store, grant := seedReplayGrant(t)
	for _, id := range []int64{101, 102} {
		seedReadyRepository(t, store, id, testSHA('a'))
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles(user_id, administrator) values($1, true)`, grant.UserID); err != nil {
		t.Fatal(err)
	}
	principal, err := store.OAuthPrincipal(t.Context(), grant.AccessHash, grant.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Administrator {
		t.Fatal("OAuth token inherited administrator privileges")
	}
	if principal.Method != authn.ProviderOAuthToken || !slices.Equal(principal.RepositoryIDs, []int64{101, 102}) {
		t.Fatalf("read scope lost: principal=%+v", principal)
	}
	if err := (&admin.Service{}).Reconcile(t.Context(), principal); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("reconcile error=%v, want forbidden", err)
	}
	scip := &scipgraph.Service{}
	if err := scip.ValidateUpload(t.Context(), principal, 101, testSHA('a')); !errors.Is(err, scipgraph.ErrForbidden) {
		t.Fatalf("SCIP upload error=%v, want forbidden", err)
	}
	if err := scip.SetDependencies(t.Context(), principal, 101, api.RepositoryPackages{}); !errors.Is(err, scipgraph.ErrForbidden) {
		t.Fatalf("SCIP dependencies error=%v, want forbidden", err)
	}
	if _, err := scip.RefreshGitHubDependencies(t.Context(), principal, 101); !errors.Is(err, scipgraph.ErrForbidden) {
		t.Fatalf("SCIP refresh error=%v, want forbidden", err)
	}
}
