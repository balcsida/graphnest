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

func TestRefreshRejectsInvalidGitHubCredentialWithoutIssuingTokens(t *testing.T) {
	for _, test := range []struct {
		name       string
		ciphertext bool
		revoked    bool
	}{
		{name: "GitHub rejects credential", ciphertext: true, revoked: true},
		{name: "GitHub credential is missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newHarness(t)
			harness.github.err = unauthorizedError{}
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
			if test.ciphertext {
				grant.GitHubTokenCiphertext, err = harness.sealer.Seal(nil, grantID, "gho_user")
				if err != nil {
					t.Fatal(err)
				}
			}
			before := *grant
			before.GitHubTokenCiphertext = append([]byte(nil), grant.GitHubTokenCiphertext...)
			harness.store.github[grant.UserID] = []int64{101}

			response, body := harness.exchange(t, url.Values{
				"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grant.ClientID},
			})
			if response.Code != http.StatusServiceUnavailable || body["error"] != "temporarily_unavailable" || body["access_token"] != nil || body["refresh_token"] != nil {
				t.Fatalf("status=%d body=%v", response.Code, body)
			}
			if test.revoked {
				if grant.RevokedAt == nil || grant.GitHubTokenCiphertext != nil {
					t.Fatalf("rejected credential did not revoke grant: %+v", *grant)
				}
			} else if grant.RevokedAt != nil || grant.AccessHash != before.AccessHash || grant.RefreshHash != before.RefreshHash || !bytes.Equal(grant.GitHubTokenCiphertext, before.GitHubTokenCiphertext) {
				t.Fatalf("missing credential changed grant: before=%+v after=%+v", before, *grant)
			}
			if got := harness.store.github[grant.UserID]; !slices.Equal(got, []int64{101}) {
				t.Fatalf("shared GitHub grants changed: %v", got)
			}
			if !test.ciphertext && len(harness.github.tokens) != 0 {
				t.Fatalf("missing credential called GitHub with %v", harness.github.tokens)
			}
		})
	}
}
