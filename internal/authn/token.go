package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/grepnest/grepnest/internal/audit"
)

const apiTokenPrefix = "gnp_"

type APITokenStore interface {
	CreateAPIToken(context.Context, APITokenRecord) (int64, error)
	CreateAPITokenAudited(context.Context, APITokenRecord, audit.Event) (int64, error)
	APIPrincipal(context.Context, [32]byte, time.Time) (Principal, error)
	RevokeAPIToken(context.Context, int64, int64) error
	RevokeAPITokenAudited(context.Context, int64, int64, audit.Event) error
}

type TokenManager struct {
	Store APITokenStore
	Now   func() time.Time
	Rand  io.Reader
	Audit audit.Recorder
}

func (m TokenManager) Create(ctx context.Context, userID int64, repositoryIDs []int64, expiresAt *time.Time) (int64, string, error) {
	return m.CreateWithMethod(ctx, userID, "oidc", repositoryIDs, expiresAt)
}

func (m TokenManager) CreateWithMethod(ctx context.Context, userID int64, method string, repositoryIDs []int64, expiresAt *time.Time) (int64, string, error) {
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
	record := APITokenRecord{
		TokenHash: sha256.Sum256([]byte(plaintext)), Prefix: plaintext[:12], UserID: userID,
		RepositoryIDs: append([]int64(nil), repositoryIDs...), CreatedAt: now, ExpiresAt: expiry,
	}
	var id int64
	var err error
	id, err = m.Store.CreateAPITokenAudited(ctx, record, audit.Event{
		ActorType: "user", ActorID: strconv.FormatInt(userID, 10), TargetType: "api_token",
		AuthenticationMethod: method, Operation: audit.OperationAPITokenCreated, Outcome: "success",
	})
	if err != nil {
		return 0, "", err
	}
	return id, plaintext, nil
}

func (m TokenManager) Authenticate(ctx context.Context, plaintext string) (Principal, error) {
	if m.Store == nil || !canonicalAPIToken(plaintext) {
		m.rejected(ctx)
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	principal, err := m.Store.APIPrincipal(ctx, sha256.Sum256([]byte(plaintext)), now)
	if err != nil {
		m.rejected(ctx)
		return Principal{}, ErrUnauthenticated
	}
	return clonePrincipal(principal), nil
}

func (m TokenManager) rejected(ctx context.Context) {
	if m.Audit != nil {
		_ = m.Audit.Record(ctx, audit.Event{
			ActorType: "anonymous", TargetType: "api_token",
			AuthenticationMethod: "api_token", Operation: audit.OperationAPITokenUseRejected,
			Outcome: "denied",
		})
	}
}

func canonicalAPIToken(token string) bool {
	if !strings.HasPrefix(token, apiTokenPrefix) || len(token) != len(apiTokenPrefix)+base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, apiTokenPrefix))
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == strings.TrimPrefix(token, apiTokenPrefix)
}
