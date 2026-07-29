package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/sso"
)

type sessionRevoker interface {
	Revoke(context.Context, string) error
}

func RegisterAuth(mux *http.ServeMux, tokenLogin bool, providers []sso.Provider, authenticator authn.RequestAuthenticator, sessions sessionRevoker, metrics *observability.Metrics) {
	metadata := make([]sso.Metadata, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		metadata = append(metadata, provider.Metadata())
		provider.Register(mux)
	}
	mux.Handle("/v1/auth/config", privateAuth(exactMethod(http.MethodGet, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeBoundedJSON(writer, struct {
			TokenLogin bool           `json:"token_login"`
			Providers  []sso.Metadata `json:"providers"`
		}{tokenLogin, metadata}, 64<<10)
	}))))
	mux.Handle("/v1/auth/session", privateAuth(exactMethod(http.MethodGet, AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal := PrincipalFromContext(request.Context())
		writeBoundedJSON(writer, struct {
			Method string `json:"method"`
		}{principal.Method}, 4<<10)
	})))))
	mux.Handle("/auth/logout", privateAuth(exactMethod(http.MethodPost, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token, count := namedCookie(request, authn.SessionCookieName)
		if count > 0 && (count != 1 || len(request.Header.Values("Authorization")) > 0 || request.Header.Get("Origin") != authenticator.PublicOrigin) {
			writeUnauthenticated(writer)
			return
		}
		if count == 1 && sessions != nil {
			if err := sessions.Revoke(request.Context(), token); err != nil {
				if errors.Is(err, authn.ErrUnauthenticated) {
					http.SetCookie(writer, sso.ClearSessionCookie())
					if metrics != nil {
						metrics.ObserveAuth("session", "logout", "invalid")
					}
					writer.WriteHeader(http.StatusNoContent)
					return
				}
				if metrics != nil {
					metrics.ObserveAuth("session", "logout", "error")
				}
				writeError(writer, http.StatusServiceUnavailable, "unavailable", "service unavailable", true)
				return
			}
		}
		http.SetCookie(writer, sso.ClearSessionCookie())
		if metrics != nil {
			metrics.ObserveAuth("session", "logout", "success")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))))
}

func privateAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(writer, request)
	})
}

func namedCookie(request *http.Request, name string) (string, int) {
	var value string
	count := 0
	for _, cookie := range request.Cookies() {
		if cookie.Name == name {
			value = cookie.Value
			count++
		}
	}
	return value, count
}
