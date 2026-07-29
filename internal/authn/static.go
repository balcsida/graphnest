package authn

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Principal struct {
	Subject         string
	Method          string
	Administrator   bool
	InstallationID  int64
	RepositoryIDs   []int64
	RepositoryNames []string
}

type Authenticator interface {
	Authenticate(context.Context, string) (Principal, error)
}

type Static struct{ principals map[string]Principal }

func NewStatic(principals map[string]Principal) *Static {
	copy := make(map[string]Principal, len(principals))
	for token, principal := range principals {
		principal.RepositoryIDs = append([]int64(nil), principal.RepositoryIDs...)
		principal.RepositoryNames = append([]string(nil), principal.RepositoryNames...)
		copy[token] = principal
	}
	return &Static{principals: copy}
}

func (auth *Static) Authenticate(_ context.Context, token string) (Principal, error) {
	presented := sha256.Sum256([]byte(token))
	var principal Principal
	matched := 0
	for configured, candidate := range auth.principals {
		expected := sha256.Sum256([]byte(configured))
		equal := subtle.ConstantTimeCompare(presented[:], expected[:])
		if equal == 1 {
			principal = candidate
		}
		matched |= equal
	}
	if matched != 1 {
		return Principal{}, ErrUnauthenticated
	}
	return clonePrincipal(principal), nil
}

func clonePrincipal(principal Principal) Principal {
	principal.RepositoryIDs = append([]int64(nil), principal.RepositoryIDs...)
	principal.RepositoryNames = append([]string(nil), principal.RepositoryNames...)
	return principal
}
