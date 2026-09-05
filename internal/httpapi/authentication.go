package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/balcsida/graphnest/internal/authn"
)

func AuthenticateRequest(authenticator authn.RequestAuthenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, err := authenticator.AuthenticateRequest(request)
		if err != nil || principal.ForceRotation {
			writeUnauthenticated(writer)
			return
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal)))
	})
}

func AuthenticateBearer(authenticator authn.Authenticator, next http.Handler) http.Handler {
	return AuthenticateBearerWithChallenge(authenticator, nil, next)
}

// BearerChallenge decorates a 401 with a WWW-Authenticate header. invalidToken
// is true when a credential was presented and rejected, false when none was.
type BearerChallenge func(writer http.ResponseWriter, invalidToken bool)

// AuthenticateBearerWithChallenge is AuthenticateBearer with an optional OAuth
// discovery challenge on every 401 (RFC 9728 §5.1), so MCP clients learn where
// to obtain a token.
func AuthenticateBearerWithChallenge(authenticator authn.Authenticator, challenge BearerChallenge, next http.Handler) http.Handler {
	reject := func(writer http.ResponseWriter, invalidToken bool) {
		if challenge != nil {
			challenge(writer, invalidToken)
		}
		writeUnauthenticated(writer)
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if hasSessionCookie(request) {
			reject(writer, false)
			return
		}
		values := request.Header.Values("Authorization")
		if len(values) != 1 {
			reject(writer, false)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			reject(writer, false)
			return
		}
		principal, err := authenticator.Authenticate(request.Context(), parts[1])
		if err != nil {
			reject(writer, true)
			return
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal)))
	})
}

func hasSessionCookie(request *http.Request) bool {
	for _, cookie := range request.Cookies() {
		if cookie.Name == authn.SessionCookieName {
			return true
		}
	}
	return false
}

func writeUnauthenticated(writer http.ResponseWriter) {
	writeError(writer, http.StatusUnauthorized, "unauthenticated", "authentication required", false)
}
