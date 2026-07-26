package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/grepnest/grepnest/internal/authn"
)

type principalContextKey struct{}

func PrincipalFromContext(ctx context.Context) authn.Principal {
	principal, _ := ctx.Value(principalContextKey{}).(authn.Principal)
	return principal
}

func AuthenticateRequest(authenticator authn.RequestAuthenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, err := authenticator.AuthenticateRequest(request)
		if err != nil {
			writeUnauthenticated(writer)
			return
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal)))
	})
}

func AuthenticateBearer(authenticator authn.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if hasSessionCookie(request) {
			writeUnauthenticated(writer)
			return
		}
		values := request.Header.Values("Authorization")
		if len(values) != 1 {
			writeUnauthenticated(writer)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeUnauthenticated(writer)
			return
		}
		principal, err := authenticator.Authenticate(parts[1])
		if err != nil {
			writeUnauthenticated(writer)
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
