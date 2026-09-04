package authn

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"
)

// oauthAccessTokenPrefix mirrors oauthas.AccessTokenPrefix; authn cannot import
// oauthas (it would be a cycle), and the bearer path only needs the shape.
const oauthAccessTokenPrefix = "gno_"

// OAuthPrincipalStore resolves a live OAuth access token hash.
type OAuthPrincipalStore interface {
	OAuthPrincipal(context.Context, [32]byte, time.Time) (Principal, error)
}

// OAuthTokenAuthenticator authenticates bearer credentials issued by the MCP
// authorization server.
type OAuthTokenAuthenticator struct {
	Store OAuthPrincipalStore
	Now   func() time.Time
}

// IsOAuthAccessToken reports whether a bearer credential has the shape of an
// OAuth access token (prefix plus exactly 32 base64url bytes).
func IsOAuthAccessToken(token string) bool {
	_, ok := oauthAccessTokenHash(token)
	return ok
}

func oauthAccessTokenHash(token string) ([32]byte, bool) {
	if !strings.HasPrefix(token, oauthAccessTokenPrefix) {
		return [32]byte{}, false
	}
	encoded := strings.TrimPrefix(token, oauthAccessTokenPrefix)
	if len(encoded) != base64.RawURLEncoding.EncodedLen(32) {
		return [32]byte{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}

func (a OAuthTokenAuthenticator) Authenticate(ctx context.Context, token string) (Principal, error) {
	hash, ok := oauthAccessTokenHash(token)
	if !ok || a.Store == nil {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	principal, err := a.Store.OAuthPrincipal(ctx, hash, now)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return clonePrincipal(principal), nil
}

// BearerRouter picks an authenticator by credential shape so each bearer
// token costs one lookup: OAuth access tokens go to OAuth, everything else to
// the API-token authenticator.
type BearerRouter struct {
	APITokens Authenticator
	OAuth     Authenticator
}

func (r BearerRouter) Authenticate(ctx context.Context, token string) (Principal, error) {
	if IsOAuthAccessToken(token) {
		if r.OAuth == nil {
			return Principal{}, ErrUnauthenticated
		}
		return r.OAuth.Authenticate(ctx, token)
	}
	if r.APITokens == nil {
		return Principal{}, ErrUnauthenticated
	}
	return r.APITokens.Authenticate(ctx, token)
}
