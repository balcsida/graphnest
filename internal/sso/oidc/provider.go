package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/sso"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

type ProviderClient interface {
	AuthorizationURL(state, nonce, verifier string) string
	Exchange(context.Context, string, string, string) (authn.Identity, error)
}

type Provider struct {
	Client   ProviderClient
	Store    authn.SessionStore
	Mapper   authn.ScopeMapper
	Sessions *authn.SessionManager
	LoginTTL time.Duration
	Metrics  *observability.Metrics
	Now      func() time.Time
	Rand     io.Reader
}

func (*Provider) Metadata() sso.Metadata {
	return sso.Metadata{ID: "oidc", Label: "Sign in with SSO", LoginURL: "/auth/oidc/login"}
}

func (provider *Provider) Register(mux *http.ServeMux) {
	mux.Handle("/auth/oidc/login", getOnly(http.HandlerFunc(provider.login)))
	mux.Handle("/auth/oidc/callback", getOnly(http.HandlerFunc(provider.callback)))
}

func (provider *Provider) login(writer http.ResponseWriter, request *http.Request) {
	privateHeaders(writer)
	state, stateRaw, err := provider.randomToken()
	if err != nil {
		provider.observe("login_start", "error")
		provider.fail(writer)
		return
	}
	browser, browserRaw, err := provider.randomToken()
	if err != nil {
		provider.observe("login_start", "error")
		provider.fail(writer)
		return
	}
	nonce, _, err := provider.randomToken()
	if err != nil {
		provider.observe("login_start", "error")
		provider.fail(writer)
		return
	}
	now := provider.now()
	expires := now.Add(provider.LoginTTL)
	verifier := oauth2.GenerateVerifier()
	flow := authn.LoginFlow{
		StateHash: sha256.Sum256(stateRaw), BrowserHash: sha256.Sum256(browserRaw),
		Provider: "oidc", Nonce: nonce, CodeVerifier: verifier, ReturnTo: "/",
		CreatedAt: now, ExpiresAt: expires,
	}
	if provider.Client == nil || provider.Store == nil || provider.LoginTTL <= 0 ||
		provider.Store.CreateLoginFlow(request.Context(), flow) != nil {
		provider.observe("login_start", "error")
		provider.fail(writer)
		return
	}
	provider.observe("login_start", "success")
	http.SetCookie(writer, sso.OIDCLoginCookie(browser, expires, now))
	http.Redirect(writer, request, provider.Client.AuthorizationURL(state, nonce, verifier), http.StatusSeeOther)
}

func (provider *Provider) callback(writer http.ResponseWriter, request *http.Request) {
	privateHeaders(writer)
	http.SetCookie(writer, sso.ClearOIDCLoginCookie())
	query := request.URL.Query()
	state, ok := exactlyOne(query["state"])
	if !ok {
		provider.callbackFail(writer, "invalid")
		return
	}
	codeValues, codePresent := query["code"]
	errorValues, errorPresent := query["error"]
	code, validCode := exactlyOne(codeValues)
	oauthError, validError := exactlyOne(errorValues)
	if !((validCode && !errorPresent) || (validError && !codePresent)) {
		provider.callbackFail(writer, "invalid")
		return
	}
	stateHash, ok := tokenHash(state)
	if !ok {
		provider.callbackFail(writer, "invalid")
		return
	}
	browser, count := cookieValue(request, sso.OIDCLoginCookieName)
	browserHash, validBrowser := tokenHash(browser)
	if count != 1 || !validBrowser {
		provider.callbackFail(writer, "invalid")
		return
	}
	if provider.Store == nil {
		provider.callbackFail(writer, "error")
		return
	}
	flow, err := provider.Store.ConsumeLoginFlow(request.Context(), stateHash, browserHash, "oidc", provider.now())
	if err != nil {
		result := "error"
		if errors.Is(err, pgx.ErrNoRows) {
			result = "invalid"
		}
		provider.callbackFail(writer, result)
		return
	}
	if validError {
		_ = oauthError
		provider.callbackFail(writer, "denied")
		return
	}
	if provider.Client == nil || provider.Sessions == nil {
		provider.callbackFail(writer, "error")
		return
	}
	identity, err := provider.Client.Exchange(request.Context(), code, flow.CodeVerifier, flow.Nonce)
	if err != nil {
		provider.callbackFail(writer, "invalid")
		return
	}
	principal, err := provider.Mapper.Map(identity)
	if err != nil {
		result := "invalid"
		if errors.Is(err, authn.ErrIdentityForbidden) {
			result = "denied"
		}
		provider.callbackFail(writer, result)
		return
	}
	token, expires, err := provider.Sessions.Create(request.Context(), identity, principal)
	if err != nil {
		provider.callbackFail(writer, "error")
		return
	}
	provider.observe("callback", "success")
	http.SetCookie(writer, sso.SessionCookie(token, expires, provider.now()))
	http.Redirect(writer, request, "/", http.StatusSeeOther)
}

func (provider *Provider) randomToken() (string, []byte, error) {
	raw := make([]byte, 32)
	reader := provider.Rand
	if reader == nil {
		reader = rand.Reader
	}
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", nil, err
	}
	return base64.RawURLEncoding.EncodeToString(raw), raw, nil
}

func (provider *Provider) now() time.Time {
	if provider.Now != nil {
		return provider.Now()
	}
	return time.Now()
}

func (*Provider) fail(writer http.ResponseWriter) {
	writer.Header().Set("Location", "/?auth_error=authentication_failed")
	writer.WriteHeader(http.StatusSeeOther)
}

func (provider *Provider) callbackFail(writer http.ResponseWriter, result string) {
	provider.observe("callback", result)
	provider.fail(writer)
}

func (provider *Provider) observe(event, result string) {
	if provider.Metrics != nil {
		provider.Metrics.ObserveAuth("oidc", event, result)
	}
}

func exactlyOne(values []string) (string, bool) {
	returnValue := ""
	if len(values) == 1 {
		returnValue = values[0]
	}
	return returnValue, len(values) == 1 && returnValue != ""
}

func tokenHash(value string) ([32]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}

func cookieValue(request *http.Request, name string) (string, int) {
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

func privateHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
}

func getOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			privateHeaders(writer)
			writer.Header().Set("Allow", http.MethodGet)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(writer, request)
	})
}
