package oauthas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

type overlappingGitHubAccess struct {
	mu           sync.Mutex
	calls        int
	firstEntered chan struct{}
	releaseFirst chan struct{}
}

func (github *overlappingGitHubAccess) AccessibleRepositories(ctx context.Context, _ string) ([]int64, error) {
	github.mu.Lock()
	github.calls++
	call := github.calls
	github.mu.Unlock()
	if call == 1 {
		close(github.firstEntered)
		select {
		case <-github.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []int64{101}, nil
	}
	return []int64{102}, nil
}

func TestLosingRefreshCannotOverwriteWinningGitHubSnapshot(t *testing.T) {
	harness := newHarness(t)
	github := &overlappingGitHubAccess{firstEntered: make(chan struct{}), releaseFirst: make(chan struct{})}
	harness.server.GitHub = github
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
	harness.store.github[grant.UserID] = []int64{999}

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- refreshRequest(harness, refresh, grant.ClientID) }()
	<-github.firstEntered
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { second <- refreshRequest(harness, refresh, grant.ClientID) }()
	secondResponse := <-second
	close(github.releaseFirst)
	firstResponse := <-first

	if secondResponse.Code != http.StatusOK || firstResponse.Code != http.StatusBadRequest {
		t.Fatalf("winning status=%d losing status=%d", secondResponse.Code, firstResponse.Code)
	}
	if got := harness.store.github[grant.UserID]; len(got) != 1 || got[0] != 102 {
		t.Fatalf("GitHub grants=%v, want winning snapshot [102]", got)
	}
}

func refreshRequest(harness *harness, refresh, clientID string) *httptest.ResponseRecorder {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}
	request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return harness.do(request)
}
