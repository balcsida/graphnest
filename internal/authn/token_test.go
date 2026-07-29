package authn

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

type tokenStoreStub struct {
	record    APITokenRecord
	principal Principal
	err       error
	id        int64
}

func (s *tokenStoreStub) CreateAPIToken(_ context.Context, record APITokenRecord) (int64, error) {
	s.record = record
	return s.id, s.err
}

func (s *tokenStoreStub) APIPrincipal(_ context.Context, hash [32]byte, _ time.Time) (Principal, error) {
	if hash != s.record.TokenHash {
		return Principal{}, errors.New("unknown token")
	}
	return s.principal, s.err
}

func (s *tokenStoreStub) RevokeAPIToken(context.Context, int64, int64) error { return nil }

func TestTokenManagerCreatesOpaqueTokenAndStoresOnlyHash(t *testing.T) {
	// Break caught: storing or returning a predictable/plaintext credential.
	store := &tokenStoreStub{id: 7}
	now := time.Unix(1, 0).UTC()
	manager := TokenManager{Store: store, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}
	repositoryIDs := []int64{101}
	expiresAt := now.Add(time.Hour)

	id, plaintext, err := manager.Create(t.Context(), 11, repositoryIDs, &expiresAt)
	if err != nil || id != 7 || !strings.HasPrefix(plaintext, "gnp_") || len(strings.TrimPrefix(plaintext, "gnp_")) != 43 {
		t.Fatalf("id=%d token=%q err=%v", id, plaintext, err)
	}
	if store.record.TokenHash != sha256.Sum256([]byte(plaintext)) || strings.Contains(string(store.record.TokenHash[:]), plaintext) || store.record.Prefix != plaintext[:12] || store.record.UserID != 11 || store.record.CreatedAt != now || store.record.ExpiresAt == nil || !store.record.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("record=%#v", store.record)
	}
	repositoryIDs[0] = 999
	expiresAt = now.Add(2 * time.Hour)
	if store.record.RepositoryIDs[0] != 101 || !store.record.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("store record mutated=%#v", store.record)
	}
}

func TestTokenManagerAuthenticatesOnlyCanonicalLiveToken(t *testing.T) {
	// Break caught: malformed, expired, revoked, or stale credentials being accepted.
	store := &tokenStoreStub{principal: Principal{Subject: "11", RepositoryIDs: []int64{101}}}
	manager := TokenManager{Store: store, Rand: strings.NewReader(strings.Repeat("z", 32))}
	_, token, err := manager.Create(t.Context(), 11, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{"", "token", "gnp_", "gnp_" + strings.Repeat("!", 43), token + "x"} {
		if _, err := manager.Authenticate(malformed); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("token=%q err=%v", malformed, err)
		}
	}
	principal, err := manager.Authenticate(token)
	if err != nil || principal.Subject != "11" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	principal.RepositoryIDs[0] = 999
	if store.principal.RepositoryIDs[0] != 101 {
		t.Fatalf("store principal mutated=%#v", store.principal)
	}
	store.err = errors.New("revoked")
	if _, err := manager.Authenticate(token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked err=%v", err)
	}
}
