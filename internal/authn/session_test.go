package authn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

type sessionStoreStub struct {
	boundIssuer, boundSubject, boundLinkID string
	session                                SessionRecord
	principal                              Principal
	lookupHash                             [32]byte
	lookupNow, lookupIdleUntil             time.Time
}

func (s *sessionStoreStub) BindOIDCUser(_ context.Context, issuer, subject, linkID string) (int64, error) {
	s.boundIssuer, s.boundSubject, s.boundLinkID = issuer, subject, linkID
	return 42, nil
}

func (s *sessionStoreStub) CreateLoginFlow(context.Context, LoginFlow) error { return nil }
func (s *sessionStoreStub) ConsumeLoginFlow(context.Context, [32]byte, [32]byte, string, time.Time) (LoginFlow, error) {
	return LoginFlow{}, nil
}
func (s *sessionStoreStub) CreateSession(_ context.Context, session SessionRecord) error {
	s.session = session
	return nil
}
func (s *sessionStoreStub) SessionPrincipal(_ context.Context, hash [32]byte, now, idleUntil time.Time) (Principal, error) {
	s.lookupHash, s.lookupNow, s.lookupIdleUntil = hash, now, idleUntil
	return s.principal, nil
}
func (s *sessionStoreStub) RevokeSession(context.Context, [32]byte) error { return nil }
func (s *sessionStoreStub) DeleteExpiredAuth(context.Context, time.Time) (int64, int64, error) {
	return 0, 0, nil
}

func TestSessionManagerCreatesOpaqueTokenForExactLinkID(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	store := &sessionStoreStub{}
	manager := SessionManager{Store: store, IdleTTL: 30 * time.Minute, TTL: 8 * time.Hour, Now: func() time.Time { return now }, Rand: bytes.NewReader(bytes.Repeat([]byte{7}, 32))}
	token, expiresAt, err := manager.Create(t.Context(), Identity{Provider: "oidc", Issuer: "https://issuer.example.test", Subject: "subject", LinkID: "directory-42", DisplayName: "Ada"})
	if err != nil || expiresAt != now.Add(8*time.Hour) {
		t.Fatalf("token=%q expiresAt=%v err=%v", token, expiresAt, err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || !bytes.Equal(raw, bytes.Repeat([]byte{7}, 32)) {
		t.Fatalf("opaque token=%q raw=%x err=%v", token, raw, err)
	}
	if store.boundIssuer != "https://issuer.example.test" || store.boundSubject != "subject" || store.boundLinkID != "directory-42" {
		t.Fatalf("bound identity = %#v", store)
	}
	if store.session.UserID != 42 || store.session.Provider != "oidc" || store.session.TokenHash != sha256.Sum256(raw) || store.session.CreatedAt != now || store.session.LastSeenAt != now || store.session.IdleExpiresAt != now.Add(30*time.Minute) || store.session.ExpiresAt != now.Add(8*time.Hour) {
		t.Fatalf("session = %#v", store.session)
	}
}

func TestSessionManagerAuthenticatesWithHashAndFreshExpiry(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	raw := bytes.Repeat([]byte{9}, 32)
	token := base64.RawURLEncoding.EncodeToString(raw)
	store := &sessionStoreStub{principal: Principal{Subject: "42", RepositoryIDs: []int64{101}}}
	manager := SessionManager{Store: store, IdleTTL: 30 * time.Minute, Now: func() time.Time { return now }}
	principal, err := manager.Authenticate(t.Context(), token)
	if err != nil || principal.Subject != "42" || principal.RepositoryIDs[0] != 101 {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	if store.lookupHash != sha256.Sum256(raw) || store.lookupNow != now || store.lookupIdleUntil != now.Add(30*time.Minute) {
		t.Fatalf("lookup hash=%x now=%v idleUntil=%v", store.lookupHash, store.lookupNow, store.lookupIdleUntil)
	}
	principal.RepositoryIDs[0] = 999
	if store.principal.RepositoryIDs[0] != 101 {
		t.Fatalf("stored repositories mutated: %#v", store.principal.RepositoryIDs)
	}
}
