package oauthas

import (
	"bytes"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

func TestRefreshRequiresReadableGitHubCredentialsBeforeRotation(t *testing.T) {
	tests := []struct {
		name      string
		breakSeal func(*testing.T, *harness, *authn.OAuthGrant)
		restore   bool
	}{
		{
			name: "wrong key",
			breakSeal: func(t *testing.T, harness *harness, _ *authn.OAuthGrant) {
				wrong, err := NewSealer(bytes.Repeat([]byte{1}, 32))
				if err != nil {
					t.Fatal(err)
				}
				harness.server.Sealer = wrong
			},
			restore: true,
		},
		{
			name: "corrupt ciphertext",
			breakSeal: func(_ *testing.T, _ *harness, grant *authn.OAuthGrant) {
				grant.GitHubTokenCiphertext = []byte("corrupt")
			},
		},
		{
			name: "missing sealer",
			breakSeal: func(_ *testing.T, harness *harness, _ *authn.OAuthGrant) {
				harness.server.Sealer = nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHarness(t)
			refresh, refreshHash, err := newSecret(nil, RefreshTokenPrefix)
			if err != nil {
				t.Fatal(err)
			}
			grantID, err := harness.store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
				ClientID: "gnc_owner", UserID: 11, Scope: "mcp", AccessHash: [32]byte{1},
				AccessExpiresAt: harness.clock.Add(time.Hour), RefreshHash: refreshHash,
				CreatedAt: harness.clock, ExpiresAt: harness.clock.Add(30 * 24 * time.Hour),
			})
			if err != nil {
				t.Fatal(err)
			}
			grant := harness.store.grants[grantID]
			grant.GitHubTokenCiphertext, err = harness.sealer.Seal(nil, grantID, "gho_user")
			if err != nil {
				t.Fatal(err)
			}
			harness.store.github[grant.UserID] = []int64{101}
			test.breakSeal(t, harness, grant)
			before := *grant
			before.GitHubTokenCiphertext = append([]byte(nil), grant.GitHubTokenCiphertext...)

			response, body := harness.exchange(t, url.Values{
				"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grant.ClientID},
			})

			if response.Code != http.StatusServiceUnavailable || body["error"] != "temporarily_unavailable" || body["access_token"] != nil || body["refresh_token"] != nil {
				t.Fatalf("status=%d body=%v, want credential failure before token issuance", response.Code, body)
			}
			if grant.AccessHash != before.AccessHash || grant.RefreshHash != before.RefreshHash || grant.PreviousRefreshHash != nil || grant.RevokedAt != nil || !bytes.Equal(grant.GitHubTokenCiphertext, before.GitHubTokenCiphertext) {
				t.Fatalf("failed refresh changed grant: before=%+v after=%+v", before, *grant)
			}
			if repositories := harness.store.github[grant.UserID]; !slices.Equal(repositories, []int64{101}) {
				t.Fatalf("failed refresh changed GitHub grants: %v", repositories)
			}
			if len(harness.github.tokens) != 0 || len(harness.audit.events) != 0 {
				t.Fatalf("failed refresh called GitHub with %v or audited %v", harness.github.tokens, harness.audit.operations())
			}

			if test.restore {
				harness.server.Sealer = harness.sealer
				response, body = harness.exchange(t, url.Values{
					"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grant.ClientID},
				})
				if response.Code != http.StatusOK || body["access_token"] == nil || body["refresh_token"] == nil {
					t.Fatalf("refresh after restoring key status=%d body=%v", response.Code, body)
				}
			}
		})
	}
}
