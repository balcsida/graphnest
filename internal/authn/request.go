package authn

import (
	"context"
	"net/http"
	"strings"

	"github.com/balcsida/graphnest/internal/observability"
)

const SessionCookieName = "__Host-grepnest_session"

type RequestAuthenticator struct {
	Bearer  Authenticator
	Session interface {
		Authenticate(context.Context, string) (Principal, error)
	}
	PublicOrigin string
	Metrics      *observability.Metrics
}

func (a RequestAuthenticator) AuthenticateRequest(request *http.Request) (Principal, error) {
	authorization := request.Header.Values("Authorization")
	session, sessionCount := requestSessionCookie(request)
	provider := "unknown"
	if len(authorization) > 0 && sessionCount == 0 {
		provider = "static"
	} else if len(authorization) == 0 && sessionCount > 0 {
		provider = "session"
	}
	observe := func(result string) {
		if a.Metrics != nil {
			a.Metrics.ObserveAuth(provider, "session_auth", result)
		}
	}
	if len(authorization) > 0 && sessionCount > 0 {
		observe("invalid")
		return Principal{}, ErrUnauthenticated
	}
	if len(authorization) > 0 {
		if len(authorization) != 1 || a.Bearer == nil {
			observe("invalid")
			return Principal{}, ErrUnauthenticated
		}
		parts := strings.Fields(authorization[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			observe("invalid")
			return Principal{}, ErrUnauthenticated
		}
		principal, err := a.Bearer.Authenticate(request.Context(), parts[1])
		if err != nil {
			observe("invalid")
			return Principal{}, ErrUnauthenticated
		}
		observe("success")
		return principal, nil
	}
	if sessionCount != 1 || a.Session == nil || (unsafeMethod(request.Method) && (a.PublicOrigin == "" || request.Header.Get("Origin") != a.PublicOrigin)) {
		observe("invalid")
		return Principal{}, ErrUnauthenticated
	}
	principal, err := a.Session.Authenticate(request.Context(), session)
	if err != nil {
		observe("invalid")
		return Principal{}, ErrUnauthenticated
	}
	observe("success")
	return principal, nil
}

func requestSessionCookie(request *http.Request) (string, int) {
	var value string
	count := 0
	for _, cookie := range request.Cookies() {
		if cookie.Name == SessionCookieName {
			value = cookie.Value
			count++
		}
	}
	return value, count
}

func unsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}
