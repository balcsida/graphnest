package account

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
)

var ErrForbidden = errors.New("forbidden")

type Token struct {
	ID            int64      `json:"id"`
	Prefix        string     `json:"prefix"`
	RepositoryIDs []int64    `json:"repository_ids,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type Service struct{ Manager authn.TokenManager }

func (s *Service) CreateToken(ctx context.Context, principal authn.Principal, expires *time.Time, repositoryIDs []int64) (Token, string, error) {
	userID, err := userID(principal)
	if err != nil || !granted(principal.RepositoryIDs, repositoryIDs) {
		return Token{}, "", ErrForbidden
	}
	id, plaintext, err := s.Manager.Create(ctx, userID, repositoryIDs, expires)
	if err != nil {
		return Token{}, "", err
	}
	createdAt := time.Now()
	if s.Manager.Now != nil {
		createdAt = s.Manager.Now()
	}
	return Token{ID: id, Prefix: plaintext[:12], RepositoryIDs: append([]int64(nil), repositoryIDs...), CreatedAt: createdAt}, plaintext, nil
}

func (s *Service) Tokens(ctx context.Context, principal authn.Principal) ([]Token, error) {
	userID, err := userID(principal)
	if err != nil {
		return nil, ErrForbidden
	}
	store, ok := s.Manager.Store.(interface {
		ListAPITokens(context.Context, int64) ([]authn.APITokenMetadata, error)
	})
	if !ok {
		return nil, ErrForbidden
	}
	items, err := store.ListAPITokens(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := make([]Token, len(items))
	for i, item := range items {
		result[i] = Token{ID: item.ID, Prefix: item.Prefix, RepositoryIDs: append([]int64(nil), item.RepositoryIDs...), CreatedAt: item.CreatedAt, LastUsedAt: item.LastUsedAt, ExpiresAt: item.ExpiresAt}
	}
	return result, nil
}

func (s *Service) RevokeToken(ctx context.Context, principal authn.Principal, id int64) error {
	userID, err := userID(principal)
	if err != nil || id < 1 || s.Manager.Store == nil {
		return ErrForbidden
	}
	return s.Manager.Store.RevokeAPIToken(ctx, userID, id)
}

func userID(principal authn.Principal) (int64, error) {
	if principal.Method != "oidc" && principal.Method != "api_token" {
		return 0, ErrForbidden
	}
	id, err := strconv.ParseInt(principal.Subject, 10, 64)
	if err != nil || id < 1 {
		return 0, ErrForbidden
	}
	return id, nil
}

func granted(grants, requested []int64) bool {
	for _, id := range requested {
		found := false
		for _, grant := range grants {
			if id == grant {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
