package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"time"
)

type SessionManager struct {
	Store SessionStore
	TTL   time.Duration
	Now   func() time.Time
	Rand  io.Reader
}

func (m *SessionManager) Create(ctx context.Context, identity Identity, principal Principal) (string, time.Time, error) {
	if !validIdentity(identity) || principal.Administrator {
		return "", time.Time{}, ErrInvalidIdentity
	}
	random := make([]byte, 32)
	reader := m.Rand
	if reader == nil {
		reader = rand.Reader
	}
	if _, err := io.ReadFull(reader, random); err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	expiresAt := now.Add(m.TTL)
	principal = Principal{Subject: identitySubject(identity), Method: identity.Provider, InstallationID: principal.InstallationID, RepositoryIDs: append([]int64(nil), principal.RepositoryIDs...)}
	if !validSessionPrincipal(principal) {
		return "", time.Time{}, ErrInvalidIdentity
	}
	if err := m.Store.CreateSession(ctx, SessionRecord{TokenHash: sha256.Sum256(random), Provider: identity.Provider, DisplayName: identity.DisplayName, Principal: principal, CreatedAt: now, ExpiresAt: expiresAt}); err != nil {
		return "", time.Time{}, err
	}
	return base64.RawURLEncoding.EncodeToString(random), expiresAt, nil
}

func (m *SessionManager) Authenticate(ctx context.Context, token string) (Principal, error) {
	hash, ok := sessionTokenHash(token)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	session, err := m.Store.Session(ctx, hash, now)
	if err != nil || session.Principal.Administrator || !validSessionPrincipal(session.Principal) {
		return Principal{}, ErrUnauthenticated
	}
	principal := session.Principal
	principal.DisplayName = session.DisplayName
	principal.RepositoryIDs = append([]int64(nil), principal.RepositoryIDs...)
	return principal, nil
}

func (m *SessionManager) Revoke(ctx context.Context, token string) error {
	hash, ok := sessionTokenHash(token)
	if !ok {
		return ErrUnauthenticated
	}
	return m.Store.DeleteSession(ctx, hash)
}

func sessionTokenHash(token string) ([32]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}

func validSessionPrincipal(principal Principal) bool {
	return principal.Subject != "" && principal.Method != "" && !principal.Administrator
}
