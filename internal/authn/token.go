package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"strings"
	"time"
)

const apiTokenPrefix = "gnp_"

type APITokenStore interface {
	CreateAPIToken(context.Context, APITokenRecord) (int64, error)
	APIPrincipal(context.Context, [32]byte, time.Time) (Principal, error)
	RevokeAPIToken(context.Context, int64, int64) error
}

type TokenManager struct {
	Store APITokenStore
	Now   func() time.Time
	Rand  io.Reader
}

func (m TokenManager) Create(ctx context.Context, userID int64, repositoryIDs []int64, expiresAt *time.Time) (int64, string, error) {
	if m.Store == nil || userID <= 0 {
		return 0, "", ErrUnauthenticated
	}
	raw := make([]byte, 32)
	reader := m.Rand
	if reader == nil {
		reader = rand.Reader
	}
	if _, err := io.ReadFull(reader, raw); err != nil {
		return 0, "", err
	}
	plaintext := apiTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	var expiry *time.Time
	if expiresAt != nil {
		value := *expiresAt
		expiry = &value
	}
	id, err := m.Store.CreateAPIToken(ctx, APITokenRecord{
		TokenHash: sha256.Sum256([]byte(plaintext)), Prefix: plaintext[:12], UserID: userID,
		RepositoryIDs: append([]int64(nil), repositoryIDs...), CreatedAt: now, ExpiresAt: expiry,
	})
	if err != nil {
		return 0, "", err
	}
	return id, plaintext, nil
}

func (m TokenManager) Authenticate(plaintext string) (Principal, error) {
	if m.Store == nil || !canonicalAPIToken(plaintext) {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	principal, err := m.Store.APIPrincipal(context.Background(), sha256.Sum256([]byte(plaintext)), now)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return clonePrincipal(principal), nil
}

func canonicalAPIToken(token string) bool {
	if !strings.HasPrefix(token, apiTokenPrefix) || len(token) != len(apiTokenPrefix)+base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, apiTokenPrefix))
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == strings.TrimPrefix(token, apiTokenPrefix)
}
