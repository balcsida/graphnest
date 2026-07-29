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
	Store   SessionStore
	IdleTTL time.Duration
	TTL     time.Duration
	Now     func() time.Time
	Rand    io.Reader
}

type PreparedSession struct {
	Token     string
	ExpiresAt time.Time
	Record    SessionRecord
}

func (m SessionManager) Create(ctx context.Context, identity Identity) (string, time.Time, error) {
	if m.Store == nil || m.IdleTTL <= 0 || m.TTL < m.IdleTTL || !validIdentity(identity) {
		return "", time.Time{}, ErrInvalidIdentity
	}
	userID, err := m.Store.BindOIDCUser(ctx, identity.Issuer, identity.Subject, identity.LinkID)
	if err != nil {
		return "", time.Time{}, err
	}
	return m.CreateForUser(ctx, userID, identity.Provider, false)
}

func (m SessionManager) CreateForUser(ctx context.Context, userID int64, provider string, forceRotation bool) (string, time.Time, error) {
	prepared, err := m.PrepareForUser(userID, provider, forceRotation)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := m.Store.CreateSession(ctx, prepared.Record); err != nil {
		return "", time.Time{}, err
	}
	return prepared.Token, prepared.ExpiresAt, nil
}

func (m SessionManager) PrepareForUser(userID int64, provider string, forceRotation bool) (PreparedSession, error) {
	if m.Store == nil || m.IdleTTL <= 0 || m.TTL < m.IdleTTL || userID <= 0 || (provider != "oidc" && provider != "local") {
		return PreparedSession{}, ErrInvalidIdentity
	}
	random := make([]byte, 32)
	reader := m.Rand
	if reader == nil {
		reader = rand.Reader
	}
	if _, err := io.ReadFull(reader, random); err != nil {
		return PreparedSession{}, err
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	expiresAt := now.Add(m.TTL)
	record := SessionRecord{
		TokenHash: sha256.Sum256(random), UserID: userID, Provider: provider,
		ForceRotation: forceRotation,
		CreatedAt:     now, LastSeenAt: now, IdleExpiresAt: now.Add(m.IdleTTL), ExpiresAt: expiresAt,
	}
	return PreparedSession{Token: base64.RawURLEncoding.EncodeToString(random), ExpiresAt: expiresAt, Record: record}, nil
}

func (m SessionManager) Authenticate(ctx context.Context, token string) (Principal, error) {
	if m.Store == nil || m.IdleTTL <= 0 {
		return Principal{}, ErrUnauthenticated
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	principal, err := m.Store.SessionPrincipal(ctx, sha256.Sum256(raw), now, now.Add(m.IdleTTL))
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return clonePrincipal(principal), nil
}

func (m SessionManager) Revoke(ctx context.Context, token string) error {
	if m.Store == nil {
		return ErrUnauthenticated
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return ErrUnauthenticated
	}
	return m.Store.RevokeSession(ctx, sha256.Sum256(raw))
}
