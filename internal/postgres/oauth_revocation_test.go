//go:build integration

package postgres

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/oauthas"
)

func TestOAuthRevocationPersistsOnlyRealSuccessAudits(t *testing.T) {
	for _, prefix := range []string{oauthas.AccessTokenPrefix, oauthas.RefreshTokenPrefix} {
		t.Run(prefix, func(t *testing.T) {
			store, grant := seedReplayGrant(t)
			raw := []byte(strings.Repeat("a", 32))
			hash := sha256.Sum256(raw)
			token := prefix + base64.RawURLEncoding.EncodeToString(raw)
			accessHash, refreshHash := grant.AccessHash, grant.RefreshHash
			if prefix == oauthas.AccessTokenPrefix {
				accessHash = hash
			} else {
				refreshHash = hash
			}
			if _, err := store.pool.Exec(t.Context(), `update oauth_grants set access_hash=$2,refresh_hash=$3 where id=$1`, grant.ID, accessHash[:], refreshHash[:]); err != nil {
				t.Fatal(err)
			}
			server := &oauthas.Server{Store: store, Limiter: store, Audit: store}
			mux := http.NewServeMux()
			server.Register(mux)
			for _, test := range []struct {
				name, token, client string
				wantAudits          int
			}{
				{"unknown", prefix + strings.Repeat("A", 43), grant.ClientID, 0},
				{"wrong client", token, "gnc_other", 0},
				{"new revocation", token, grant.ClientID, 1},
				{"duplicate", token, grant.ClientID, 1},
			} {
				request := httptest.NewRequest(http.MethodPost, "/oauth/revoke", strings.NewReader(url.Values{"token": {test.token}, "client_id": {test.client}}.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, request)
				var audits int
				if err := store.pool.QueryRow(t.Context(), `select count(*) from audit_events where operation='oauth_grant_revoked' and outcome='success'`).Scan(&audits); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusOK || audits != test.wantAudits {
					t.Errorf("%s: status=%d audits=%d, want 200 and %d", test.name, response.Code, audits, test.wantAudits)
				}
			}
		})
	}
}
