package oauthas

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRefreshEndpointRevokesOldestTokenFamily(t *testing.T) {
	for _, rotations := range []int{1, 3} {
		t.Run(strconv.Itoa(rotations)+"_rotations", func(t *testing.T) {
			h := newHarness(t)
			const redirect = "http://127.0.0.1:5000/cb"
			clientID := h.registerClient(t, redirect)
			verifier, challenge := pkce()
			consent := h.runConsent(t, clientID, redirect, challenge, "allow")
			location, err := url.Parse(consent.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			response, tokens := h.exchange(t, url.Values{
				"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")},
				"client_id": {clientID}, "redirect_uri": {redirect}, "code_verifier": {verifier},
			})
			if response.Code != http.StatusOK {
				t.Fatalf("code exchange status=%d body=%v", response.Code, tokens)
			}
			original := tokens["refresh_token"].(string)
			firstRotation := h.clock.Add(50 * time.Minute)
			for i := range rotations {
				h.clock = firstRotation.Add(time.Duration(i) * 10 * time.Second)
				response, tokens = h.exchange(t, url.Values{
					"grant_type": {"refresh_token"}, "refresh_token": {tokens["refresh_token"].(string)}, "client_id": {clientID},
				})
				if response.Code != http.StatusOK {
					t.Fatalf("rotation %d status=%d body=%v", i+1, response.Code, tokens)
				}
			}
			currentAccess, _ := hashSecret(tokens["access_token"].(string), AccessTokenPrefix)
			currentRefresh, _ := hashSecret(tokens["refresh_token"].(string), RefreshTokenPrefix)
			for _, elapsed := range []time.Duration{25 * time.Second, refreshGrace + time.Second} {
				h.clock = firstRotation.Add(elapsed)
				if _, err := h.store.OAuthPrincipal(t.Context(), currentAccess, h.clock); err != nil {
					t.Fatalf("access before replay at %s: %v", elapsed, err)
				}
				response, body := h.exchange(t, url.Values{
					"grant_type": {"refresh_token"}, "refresh_token": {original}, "client_id": {clientID},
				})
				if response.Code != http.StatusBadRequest || body["error"] != "invalid_grant" {
					t.Fatalf("replay at %s status=%d body=%v", elapsed, response.Code, body)
				}
				_, accessErr := h.store.OAuthPrincipal(t.Context(), currentAccess, h.clock)
				_, refreshErr := h.store.OAuthGrantByRefresh(t.Context(), currentRefresh, h.clock)
				if elapsed < refreshGrace {
					if accessErr != nil || refreshErr != nil {
						t.Fatalf("within-grace replay revoked family: access=%v refresh=%v", accessErr, refreshErr)
					}
				} else if !errors.Is(accessErr, pgx.ErrNoRows) || !errors.Is(refreshErr, pgx.ErrNoRows) {
					t.Fatalf("outside-grace family still live: access=%v refresh=%v", accessErr, refreshErr)
				}
			}
			for _, grant := range h.store.grants {
				if grant.GitHubTokenCiphertext != nil {
					t.Fatal("replay retained provider credential")
				}
			}
		})
	}
}
