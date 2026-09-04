package oauthas

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

func TestRefreshReportsRemainingGrantLifetime(t *testing.T) {
	h := newHarness(t)
	client := h.registerClient(t, "http://127.0.0.1:5000/cb")
	refresh, refreshHash, err := newSecret(nil, RefreshTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		ClientID: client, UserID: 11, RefreshHash: refreshHash,
		CreatedAt: h.clock.Add(-GrantTTL + 37*time.Second), ExpiresAt: h.clock.Add(37 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, body := h.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {client}})
	if response.Code != http.StatusOK || body["expires_in"] != float64(37) {
		t.Fatalf("status=%d body=%v; token has only 37 seconds left", response.Code, body)
	}
}
