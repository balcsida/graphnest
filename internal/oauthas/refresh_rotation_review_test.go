package oauthas

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

type refreshRotationReviewStore struct {
	authn.OAuthStore
	failure error
	wrap    bool
}

func (store *refreshRotationReviewStore) RotateOAuthGrant(ctx context.Context, hash [32]byte, rotation authn.OAuthRotation) (authn.OAuthGrant, error) {
	if store.failure != nil {
		return authn.OAuthGrant{}, store.failure
	}
	grant, err := store.OAuthStore.RotateOAuthGrant(ctx, hash, rotation)
	if err != nil && store.wrap {
		return grant, fmt.Errorf("rotation store: %w", err)
	}
	return grant, err
}

func newRefreshRotationReviewGrant(t *testing.T, harness *harness) (string, *authn.OAuthGrant) {
	t.Helper()
	refresh, refreshHash, err := newSecret(nil, RefreshTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	id, err := harness.store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		ClientID: "gnc_owner", UserID: 11, AccessHash: [32]byte{1},
		AccessExpiresAt: harness.clock.Add(time.Hour), RefreshHash: refreshHash,
		CreatedAt: harness.clock, ExpiresAt: harness.clock.Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return refresh, harness.store.grants[id]
}

func TestRefreshRotationFailureIsRetryable(t *testing.T) {
	harness := newHarness(t)
	refresh, grant := newRefreshRotationReviewGrant(t, harness)
	before := *grant
	store := &refreshRotationReviewStore{OAuthStore: harness.store, failure: errors.New("database unavailable")}
	harness.server.Store = store

	response, body := harness.exchange(t, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grant.ClientID},
	})
	if response.Code != http.StatusServiceUnavailable || body["error"] != "temporarily_unavailable" || body["access_token"] != nil || body["refresh_token"] != nil {
		t.Fatalf("status=%d body=%v, want retryable rotation failure", response.Code, body)
	}
	if grant.AccessHash != before.AccessHash || grant.RefreshHash != before.RefreshHash || grant.PreviousRefreshHash != nil || grant.RevokedAt != nil {
		t.Fatalf("failed refresh changed grant: before=%+v after=%+v", before, *grant)
	}
	if len(harness.audit.operations()) != 0 {
		t.Fatalf("failed refresh recorded audit events: %v", harness.audit.operations())
	}

	store.failure = nil
	response, body = harness.exchange(t, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grant.ClientID},
	})
	if response.Code != http.StatusOK || body["access_token"] == nil || body["refresh_token"] == nil {
		t.Fatalf("recovered refresh status=%d body=%v", response.Code, body)
	}
	newRefreshHash, ok := hashSecret(body["refresh_token"].(string), RefreshTokenPrefix)
	if !ok || grant.RefreshHash != newRefreshHash || grant.PreviousRefreshHash == nil || *grant.PreviousRefreshHash != before.RefreshHash {
		t.Fatalf("retry did not rotate from the preserved refresh hash: grant=%+v", *grant)
	}
}

func TestRefreshWrappedRotationControlsRemainInvalidGrant(t *testing.T) {
	t.Run("no rows within grace", func(t *testing.T) {
		harness := newHarness(t)
		refresh, grant := newRefreshRotationReviewGrant(t, harness)
		harness.server.Store = &refreshRotationReviewStore{OAuthStore: harness.store, wrap: true}

		response, body := harness.exchange(t, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grant.ClientID},
		})
		if response.Code != http.StatusOK {
			t.Fatalf("initial refresh status=%d body=%v", response.Code, body)
		}
		beforeRetry := *grant
		harness.clock = harness.clock.Add(time.Second)
		response, body = harness.exchange(t, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grant.ClientID},
		})
		if response.Code != http.StatusBadRequest || body["error"] != "invalid_grant" || body["error_description"] != "refresh token is invalid or expired" {
			t.Fatalf("status=%d body=%v, want wrapped no-rows invalid_grant", response.Code, body)
		}
		if grant.RevokedAt != nil || grant.AccessHash != beforeRetry.AccessHash || grant.RefreshHash != beforeRetry.RefreshHash {
			t.Fatalf("no-rows failure changed grant: before=%+v after=%+v", beforeRetry, *grant)
		}
		if slices.Contains(harness.audit.operations(), OperationGrantReplay) {
			t.Fatalf("no-rows failure recorded replay audit: %v", harness.audit.operations())
		}
	})

	t.Run("replay", func(t *testing.T) {
		harness := newHarness(t)
		refresh, grant := newRefreshRotationReviewGrant(t, harness)
		harness.server.Store = &refreshRotationReviewStore{OAuthStore: harness.store, wrap: true}

		response, body := harness.exchange(t, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grant.ClientID},
		})
		if response.Code != http.StatusOK || body["refresh_token"] == nil {
			t.Fatalf("initial refresh status=%d body=%v", response.Code, body)
		}
		harness.clock = harness.clock.Add(refreshGrace + time.Second)
		response, body = harness.exchange(t, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grant.ClientID},
		})
		if response.Code != http.StatusBadRequest || body["error"] != "invalid_grant" || body["error_description"] != "refresh token was already used; the grant has been revoked" {
			t.Fatalf("status=%d body=%v, want wrapped replay invalid_grant", response.Code, body)
		}
		if grant.RevokedAt == nil {
			t.Fatalf("replay did not revoke grant: %+v", *grant)
		}
		if !slices.Contains(harness.audit.operations(), OperationGrantReplay) {
			t.Fatalf("replay missing replay audit: %v", harness.audit.operations())
		}
	})
}

var _ authn.OAuthStore = (*refreshRotationReviewStore)(nil)
