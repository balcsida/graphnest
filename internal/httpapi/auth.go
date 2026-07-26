package httpapi

import (
	"context"
	"net/http"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/sso"
)

type sessionRevoker interface {
	Revoke(context.Context, string) error
}

func RegisterAuth(
	mux *http.ServeMux,
	tokenLogin bool,
	providers []sso.Provider,
	authenticator authn.RequestAuthenticator,
	sessions sessionRevoker,
	metrics *observability.Metrics,
) {
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
			Method      string `json:"method"`
			DisplayName string `json:"display_name,omitempty"`
		}{principal.Method, principal.DisplayName}, 4<<10)
	})))))
	mux.Handle("/auth/logout", privateAuth(exactMethod(http.MethodPost, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.SetCookie(writer, sso.ClearSessionCookie())
		if token, count := namedCookie(request, authn.SessionCookieName); count == 1 && sessions != nil {
			_ = sessions.Revoke(request.Context(), token)
		}
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
