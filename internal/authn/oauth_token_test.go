package authn

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

type oauthStoreStub struct {
	hash [32]byte
	now  time.Time
	err  error
}

func (s *oauthStoreStub) OAuthPrincipal(_ context.Context, hash [32]byte, now time.Time) (Principal, error) {
	s.hash, s.now = hash, now
	if s.err != nil {
		return Principal{}, s.err
	}
	return Principal{Subject: "11", Method: ProviderOAuthToken, RepositoryIDs: []int64{101}}, nil
}

func TestOAuthTokenAuthenticatorHashesTheRandomPart(t *testing.T) {
	raw := []byte(strings.Repeat("r", 32))
	token := "gno_" + base64.RawURLEncoding.EncodeToString(raw)
	store := &oauthStoreStub{}
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	auth := OAuthTokenAuthenticator{Store: store, Now: func() time.Time { return clock }}
	principal, err := auth.Authenticate(context.Background(), token)
	if err != nil || principal.Method != ProviderOAuthToken || store.hash != sha256.Sum256(raw) || !store.now.Equal(clock) {
		t.Fatalf("principal=%+v err=%v hash-ok=%v", principal, err, store.hash == sha256.Sum256(raw))
	}
	principal.RepositoryIDs[0] = 999
	if again, _ := auth.Authenticate(context.Background(), token); again.RepositoryIDs[0] != 101 {
		t.Fatal("principal must be cloned")
	}
	store.err = errors.New("no rows")
	if _, err := auth.Authenticate(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("store miss err=%v", err)
	}
	for _, bad := range []string{"gnp_" + token[4:], "gno_short", token + "x", ""} {
		if _, err := auth.Authenticate(context.Background(), bad); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%q accepted", bad)
		}
	}
	if (OAuthTokenAuthenticator{}).Store != nil {
		t.Fatal("zero value must be unusable")
	}
	if _, err := (OAuthTokenAuthenticator{}).Authenticate(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("nil store must reject")
	}
}

func TestBearerRouterDispatchesByShape(t *testing.T) {
	apiTokens := NewStatic(map[string]Principal{"gnp_static": {Subject: "api", Method: "static"}})
	store := &oauthStoreStub{}
	router := BearerRouter{APITokens: apiTokens, OAuth: OAuthTokenAuthenticator{Store: store}}
	oauthToken := "gno_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("o", 32)))
	if principal, err := router.Authenticate(context.Background(), oauthToken); err != nil || principal.Method != ProviderOAuthToken {
		t.Fatalf("oauth token principal=%+v err=%v", principal, err)
	}
	if principal, err := router.Authenticate(context.Background(), "gnp_static"); err != nil || principal.Subject != "api" {
		t.Fatalf("api token principal=%+v err=%v", principal, err)
	}
	if _, err := (BearerRouter{APITokens: apiTokens}).Authenticate(context.Background(), oauthToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("oauth token without an oauth authenticator must be rejected, not fall through")
	}
	if _, err := (BearerRouter{OAuth: OAuthTokenAuthenticator{Store: store}}).Authenticate(context.Background(), "gnp_static"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("api token without an api authenticator must be rejected")
	}
}
