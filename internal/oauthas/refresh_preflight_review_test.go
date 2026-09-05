package oauthas

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

type refreshLookupFailureStore struct {
	authn.OAuthStore
	rotations int
}

func (store *refreshLookupFailureStore) OAuthGrantByRefresh(context.Context, [32]byte, time.Time) (authn.OAuthGrant, error) {
	return authn.OAuthGrant{}, errors.New("storage unavailable")
}

func (store *refreshLookupFailureStore) RotateOAuthGrant(ctx context.Context, hash [32]byte, rotation authn.OAuthRotation) (authn.OAuthGrant, error) {
	store.rotations++
	return store.OAuthStore.RotateOAuthGrant(ctx, hash, rotation)
}

func TestRefreshLookupFailureStopsBeforeRotation(t *testing.T) {
	harness := newHarness(t)
	refresh, refreshHash, err := newSecret(nil, RefreshTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		ClientID: "gnc_owner", UserID: 11, RefreshHash: refreshHash,
		CreatedAt: harness.clock, ExpiresAt: harness.clock.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	store := &refreshLookupFailureStore{OAuthStore: harness.store}
	harness.server.Store = store

	response, body := harness.exchange(t, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"gnc_attacker"},
	})

	if response.Code != http.StatusServiceUnavailable || body["error"] != "temporarily_unavailable" || store.rotations != 0 {
		t.Fatalf("status=%d error=%v rotations=%d, want fail closed before rotation", response.Code, body["error"], store.rotations)
	}
}
