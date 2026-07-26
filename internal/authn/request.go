package authn

import (
	"context"
	"net/http"
	"strings"
)

const SessionCookieName = "__Host-grepnest_session"

type RequestAuthenticator struct {
	Bearer  Authenticator
	Session interface {
		Authenticate(context.Context, string) (Principal, error)
	}
	PublicOrigin string
}

func (a RequestAuthenticator) AuthenticateRequest(request *http.Request) (Principal, error) {
	values := request.Header.Values("Authorization")
	session, sessionCount := requestSessionCookie(request)
	if len(values) > 0 && sessionCount > 0 {
		return Principal{}, ErrUnauthenticated
	}
	if len(values) > 0 {
		if len(values) != 1 || a.Bearer == nil {
			return Principal{}, ErrUnauthenticated
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return Principal{}, ErrUnauthenticated
		}
		principal, err := a.Bearer.Authenticate(parts[1])
		if err != nil {
			return Principal{}, ErrUnauthenticated
		}
		return principal, nil
	}
	if sessionCount != 1 || a.Session == nil || (unsafeMethod(request.Method) && request.Header.Get("Origin") != a.PublicOrigin) {
		return Principal{}, ErrUnauthenticated
	}
	principal, err := a.Session.Authenticate(request.Context(), session)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
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
