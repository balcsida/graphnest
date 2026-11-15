package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/balcsida/graphnest/internal/authn"
)

// delegationRoutePath is the only request a delegation-only token may make.
const delegationRoutePath = "/v1/admin/api-tokens"

// inventoryRoutePath is the listing an inventory-only token exists for. Such a
// token may also read the status of one repository below it, because the
// listing does not resolve SCIP or graph status.
const inventoryRoutePath = "/v1/repositories"

// confined reports whether a single-purpose principal (delegation-only or
// inventory-only) must be refused for this request. Such a principal is an
// administrator with an empty repository ceiling, which several administrative
// read paths treat as "every repository"; confining it here means no handler
// has to know about the special case: a leaked broker credential can only
// mint, and a leaked inventory credential can only read the inventory. A
// principal carrying both markers is refused everywhere.
func confined(principal authn.Principal, request *http.Request) bool {
	return principal.DelegationOnly && !(request.Method == http.MethodPost && request.URL.Path == delegationRoutePath) ||
		principal.InventoryOnly && !(request.Method == http.MethodGet && inventoryRoute(request.URL.Path))
}

// inventoryRoute matches /v1/repositories, /v1/repositories/{id} and
// /v1/repositories/{id}/status, and nothing else below that prefix.
func inventoryRoute(path string) bool {
	if path == inventoryRoutePath {
		return true
	}
	_, ok := repositoryID(path)
	return ok && strings.HasPrefix(path, inventoryRoutePath+"/")
}

func writeForbidden(writer http.ResponseWriter) {
	writeError(writer, http.StatusForbidden, "forbidden", "forbidden", false)
}

func AuthenticateRequest(authenticator authn.RequestAuthenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, err := authenticator.AuthenticateRequest(request)
		if err != nil || principal.ForceRotation {
			writeUnauthenticated(writer)
			return
		}
		if confined(principal, request) {
			writeForbidden(writer)
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
		if confined(principal, request) {
			writeForbidden(writer)
			return
		}
		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		ctx = authn.WithFreshPrincipal(ctx, func(ctx context.Context) (authn.Principal, error) {
			return authenticator.Authenticate(ctx, parts[1])
		})
		next.ServeHTTP(writer, request.WithContext(ctx))
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
