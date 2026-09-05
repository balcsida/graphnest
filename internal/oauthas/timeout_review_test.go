package oauthas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"
)

type stalledGitHubAccess struct {
	canceled chan error
}

func (github stalledGitHubAccess) AccessibleRepositories(ctx context.Context, _ string) ([]int64, error) {
	<-ctx.Done()
	github.canceled <- ctx.Err()
	return nil, ctx.Err()
}

func TestRefreshGitHubSyncFinishesBeforeWriteTimeout(t *testing.T) {
	harness := newHarness(t)
	clientID := harness.registerClient(t, "http://127.0.0.1:5000/cb")
	verifier, challenge := pkce()
	consent := harness.runConsent(t, clientID, "http://127.0.0.1:5000/cb", challenge, "allow")
	location, err := url.Parse(consent.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	response, initial := harness.exchange(t, url.Values{
		"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")},
		"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5000/cb"}, "code_verifier": {verifier},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("initial exchange status=%d body=%v", response.Code, initial)
	}
	oldRefresh := initial["refresh_token"].(string)
	refreshHash, _ := hashSecret(oldRefresh, RefreshTokenPrefix)
	grant, err := harness.store.OAuthGrantByRefresh(t.Context(), refreshHash, harness.clock)
	if err != nil {
		t.Fatal(err)
	}
	originalCiphertext := append([]byte(nil), grant.GitHubTokenCiphertext...)
	if len(originalCiphertext) == 0 {
		t.Fatal("initial grant has no GitHub ciphertext")
	}
	harness.store.mu.Lock()
	harness.store.github[grant.UserID] = []int64{101}
	harness.store.mu.Unlock()
	canceled := make(chan error, 1)
	harness.server.GitHub = stalledGitHubAccess{canceled: canceled}

	server := httptest.NewUnstartedServer(harness.mux)
	server.Config.ReadHeaderTimeout = 5 * time.Second
	server.Config.ReadTimeout = 10 * time.Second
	server.Config.WriteTimeout = 10 * time.Second
	server.Config.IdleTimeout = time.Minute
	server.Start()
	defer server.Close()
	client := server.Client()
	client.Timeout = 15 * time.Second
	started := time.Now()
	remoteResponse, err := client.PostForm(server.URL+"/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {oldRefresh}, "client_id": {clientID},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer remoteResponse.Body.Close()
	if elapsed := time.Since(started); elapsed >= server.Config.WriteTimeout/2 {
		t.Fatalf("refresh response took %s, want under %s", elapsed, server.Config.WriteTimeout/2)
	}
	var rotated map[string]any
	if err := json.NewDecoder(remoteResponse.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	if remoteResponse.StatusCode != http.StatusOK {
		t.Fatalf("refresh status=%d body=%v", remoteResponse.StatusCode, rotated)
	}
	if err := <-canceled; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GitHub cancellation=%v", err)
	}

	harness.store.mu.Lock()
	current := *harness.store.grants[grant.ID]
	repositories := append([]int64(nil), harness.store.github[grant.UserID]...)
	harness.store.mu.Unlock()
	if !bytes.Equal(current.GitHubTokenCiphertext, originalCiphertext) || !slices.Equal(repositories, []int64{101}) {
		t.Fatalf("timeout changed ciphertext=%t repositories=%v", !bytes.Equal(current.GitHubTokenCiphertext, originalCiphertext), repositories)
	}
	newRefreshHash, ok := hashSecret(rotated["refresh_token"].(string), RefreshTokenPrefix)
	if !ok || newRefreshHash != current.RefreshHash || current.RefreshHash == refreshHash {
		t.Fatal("refresh rotation did not match returned token")
	}

	response, _ = harness.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {oldRefresh}, "client_id": {clientID}})
	if response.Code != http.StatusBadRequest || harness.store.grants[grant.ID].RevokedAt != nil {
		t.Fatalf("within-grace replay status=%d revoked=%v", response.Code, harness.store.grants[grant.ID].RevokedAt != nil)
	}
	harness.clock = harness.clock.Add(refreshGrace + time.Second)
	response, _ = harness.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {oldRefresh}, "client_id": {clientID}})
	if response.Code != http.StatusBadRequest || harness.store.grants[grant.ID].RevokedAt == nil {
		t.Fatalf("after-grace replay status=%d revoked=%v", response.Code, harness.store.grants[grant.ID].RevokedAt != nil)
	}
}
