package browserflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/oauthas"
	"github.com/balcsida/graphnest/internal/sso"
	"github.com/jackc/pgx/v5"
)

var oidcSpec = Spec{
	Metadata:  sso.Metadata{ID: "oidc", Label: "Sign in with SSO", LoginURL: "/auth/oidc/login"},
	LoginPath: "/auth/oidc/login", CallbackPath: "/auth/oidc/callback",
	FlowProvider: authn.ProviderOIDC, IdentityProvider: authn.ProviderOIDC,
	CookieName: sso.OIDCLoginCookieName, Method: authn.ProviderOIDC,
	SuccessOperation: audit.OperationOIDCLoginSucceeded,
	DeniedOperation:  audit.OperationOIDCLoginDenied,
}

type failingRecorder struct{ events []audit.Event }

func (r *failingRecorder) Record(_ context.Context, event audit.Event) error {
	r.events = append(r.events, event)
	return errors.New("audit unavailable")
}

func TestCallbackDenialIgnoresAuditFailure(t *testing.T) {
	recorder := &failingRecorder{}
	provider := &Provider{Spec: oidcSpec, Audit: recorder}
	response := httptest.NewRecorder()
	provider.callback(response, httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?error=sentinel-claim", nil))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", response.Code)
	}
	if len(recorder.events) != 1 || recorder.events[0].AuthenticationMethod != authn.ProviderOIDC ||
		recorder.events[0].Operation != audit.OperationOIDCLoginDenied || strings.Contains(recorder.events[0].ActorID+recorder.events[0].TargetID, "sentinel") {
		t.Fatalf("events=%#v", recorder.events)
	}
}

type providerClient struct {
	identity authn.Identity
	err      error
	code     string
	verifier string
	nonce    string
}

func (client *providerClient) AuthorizationURL(state, nonce, verifier string) string {
	client.nonce, client.verifier = nonce, verifier
	return "https://idp.example.test/authorize?state=" + url.QueryEscape(state)
}

func (client *providerClient) Exchange(_ context.Context, code, verifier, nonce string) (authn.Identity, error) {
	client.code, client.verifier, client.nonce = code, verifier, nonce
	return client.identity, client.err
}

type providerStore struct {
	flow           authn.LoginFlow
	createErr      error
	consumeErr     error
	sessionErr     error
	principalErr   error
	consumed       bool
	consumeArgs    [][32]byte
	session        authn.SessionRecord
	loginOperation string
}

func (store *providerStore) CreateLoginFlow(_ context.Context, flow authn.LoginFlow) error {
	store.flow = flow
	return store.createErr
}
func (store *providerStore) ConsumeLoginFlow(_ context.Context, state, browser [32]byte, provider string, now time.Time) (authn.LoginFlow, error) {
	store.consumeArgs = append(store.consumeArgs, state, browser)
	if store.consumeErr != nil || store.consumed || provider != store.flow.Provider || now.Before(store.flow.CreatedAt) || !now.Before(store.flow.ExpiresAt) ||
		state != store.flow.StateHash || browser != store.flow.BrowserHash {
		if store.consumeErr != nil {
			return authn.LoginFlow{}, store.consumeErr
		}
		return authn.LoginFlow{}, pgx.ErrNoRows
	}
	store.consumed = true
	return store.flow, nil
}
func (store *providerStore) CreateSession(_ context.Context, session authn.SessionRecord) error {
	if store.sessionErr != nil {
		return store.sessionErr
	}
	store.session = session
	return nil
}
func (store *providerStore) CreateSessionAudited(ctx context.Context, session authn.SessionRecord, _ audit.Event) error {
	return store.CreateSession(ctx, session)
}
func (store *providerStore) CreateFederatedSessionAudited(ctx context.Context, identity authn.Identity, session authn.SessionRecord, operation string) error {
	store.loginOperation = operation
	userID, err := store.BindFederatedUser(ctx, identity.Issuer, identity.Subject, identity.LinkID)
	if err != nil {
		return err
	}
	session.UserID = userID
	return store.CreateSession(ctx, session)
}
func (store *providerStore) BindFederatedUser(context.Context, string, string, string) (int64, error) {
	return 1, nil
}
func (store *providerStore) SessionPrincipal(_ context.Context, hash [32]byte, _, _ time.Time) (authn.Principal, error) {
	if store.principalErr != nil {
		return authn.Principal{}, store.principalErr
	}
	if hash != store.session.TokenHash || store.session.UserID == 0 {
		return authn.Principal{}, pgx.ErrNoRows
	}
	return authn.Principal{Subject: strconv.FormatInt(store.session.UserID, 10), Method: store.session.Provider}, nil
}
func (*providerStore) RevokeSession(context.Context, [32]byte) error { return nil }
func (store *providerStore) RevokeSessionAudited(ctx context.Context, hash [32]byte) error {
	return store.RevokeSession(ctx, hash)
}
func (*providerStore) DeleteExpiredAuth(context.Context, time.Time) (int64, int64, error) {
	return 0, 0, nil
}

func TestOIDCProviderLoginFailsClosedOnEntropyOrStoreErrors(t *testing.T) {
	tests := []struct {
		name   string
		random []byte
		store  *providerStore
	}{
		{"entropy", nil, &providerStore{}},
		{"store", bytes.Repeat([]byte{1}, 96), &providerStore{createErr: errors.New("database unavailable")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &Provider{
				Spec:   oidcSpec,
				Client: &providerClient{}, Store: test.store, LoginTTL: time.Minute,
				Rand: bytes.NewReader(test.random),
			}
			mux := http.NewServeMux()
			provider.Register(mux)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/oidc/login", nil))
			if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/?auth_error=authentication_failed" ||
				len(recorder.Result().Cookies()) != 0 {
				t.Fatalf("response = %d headers=%v cookies=%#v", recorder.Code, recorder.Header(), recorder.Result().Cookies())
			}
		})
	}
}

func TestOIDCProviderLoginCreatesBoundFlowAndRedirects(t *testing.T) {
	now := time.Unix(1_000, 0)
	random := append(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)...)
	random = append(random, bytes.Repeat([]byte{3}, 32)...)
	store := &providerStore{}
	client := &providerClient{}
	provider := &Provider{
		Spec:   oidcSpec,
		Client: client, Store: store, LoginTTL: 10 * time.Minute, Now: func() time.Time { return now },
		Rand: bytes.NewReader(random), Sessions: &authn.SessionManager{Store: store, TTL: time.Hour},
	}
	mux := http.NewServeMux()
	provider.Register(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/oidc/login", nil))

	if recorder.Code != http.StatusSeeOther || !strings.HasPrefix(recorder.Header().Get("Location"), "https://idp.example.test/authorize?state=") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || !strings.HasPrefix(cookies[0].Name, sso.OIDCLoginCookieName+"_") || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookies = %#v", cookies)
	}
	state := strings.TrimPrefix(recorder.Header().Get("Location"), "https://idp.example.test/authorize?state=")
	state, _ = url.QueryUnescape(state)
	stateRaw, _ := base64.RawURLEncoding.DecodeString(state)
	browserRaw, _ := base64.RawURLEncoding.DecodeString(cookies[0].Value)
	nonceRaw, _ := base64.RawURLEncoding.DecodeString(client.nonce)
	if len(stateRaw) != 32 || len(browserRaw) != 32 || len(nonceRaw) != 32 || state == cookies[0].Value || client.nonce == state || client.verifier == "" {
		t.Fatalf("state/browser/nonce/verifier not independent")
	}
	if store.flow.StateHash != sha256.Sum256(stateRaw) || store.flow.BrowserHash != sha256.Sum256(browserRaw) ||
		store.flow.Provider != "oidc" || store.flow.Nonce != client.nonce || store.flow.CodeVerifier != client.verifier ||
		store.flow.ReturnTo != "/" || !store.flow.CreatedAt.Equal(now) || !store.flow.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("flow = %#v", store.flow)
	}
	assertPrivateHeaders(t, recorder)
}

func TestProviderUsesSpecifiedLoginCookieForLoginAndCallback(t *testing.T) {
	const cookieName = "__Host-graphnest_test_browserflow_login"
	spec := oidcSpec
	spec.CookieName = cookieName

	now := time.Unix(1_000, 0)
	store := &providerStore{}
	client := &providerClient{}
	provider := &Provider{
		Spec: spec, Client: client, Store: store, LoginTTL: time.Minute,
		Now: func() time.Time { return now }, Rand: bytes.NewReader(bytes.Repeat([]byte{1}, 96)),
	}
	mux := http.NewServeMux()
	provider.Register(mux)
	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodGet, spec.LoginPath, nil))
	if cookies := login.Result().Cookies(); len(cookies) != 1 || !strings.HasPrefix(cookies[0].Name, cookieName+"_") {
		t.Fatalf("login cookies=%#v", cookies)
	}

	fixture := newCallbackFixture(t)
	fixture.provider.Spec.CookieName = cookieName
	callback := fixture.callback(t, "?state="+fixture.state+"&code=good", fixture.browser)
	if !fixture.store.consumed {
		t.Fatal("callback did not consume the flow using the specified cookie")
	}
	cookies := callback.Result().Cookies()
	if len(cookies) != 2 || !strings.HasPrefix(cookies[0].Name, cookieName+"_") || cookies[0].MaxAge != -1 {
		t.Fatalf("callback cookies=%#v", cookies)
	}
}

func TestProviderLoginCookiesAreBoundToTheirStates(t *testing.T) {
	random := append(bytes.Repeat([]byte{1}, 96), bytes.Repeat([]byte{2}, 96)...)
	provider := &Provider{
		Spec: oidcSpec, Client: &providerClient{}, Store: &providerStore{}, LoginTTL: time.Minute,
		Now: func() time.Time { return time.Unix(1_000, 0) }, Rand: bytes.NewReader(random),
	}
	mux := http.NewServeMux()
	provider.Register(mux)
	seen := map[string]bool{}
	for range 2 {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, provider.Spec.LoginPath, nil))
		location, err := url.Parse(recorder.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		state := location.Query().Get("state")
		stateHash, ok := tokenHash(state)
		if !ok {
			t.Fatalf("invalid state %q", state)
		}
		want := provider.Spec.CookieName + "_" + hex.EncodeToString(stateHash[:])
		if cookies := recorder.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != want {
			t.Fatalf("state cookie=%#v want %q", cookies, want)
		}
		if seen[want] {
			t.Fatalf("concurrent login reused cookie %q", want)
		}
		seen[want] = true
	}
}

func TestProviderInvalidStateDoesNotClearActiveLoginCookie(t *testing.T) {
	fixture := newCallbackFixture(t)
	recorder := fixture.callback(t, "?code=good", fixture.browser)
	if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("invalid state cleared cookies: %#v", cookies)
	}
}

func TestOIDCProviderCallbackSuccessConsumesCreatesSessionAndRedirects(t *testing.T) {
	fixture := newCallbackFixture(t)
	recorder := fixture.callback(t, "?state="+fixture.state+"&code=good", fixture.browser)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if !fixture.store.consumed || fixture.client.code != "good" || fixture.client.verifier != fixture.store.flow.CodeVerifier ||
		fixture.client.nonce != fixture.store.flow.Nonce || fixture.store.session.Provider != "oidc" ||
		fixture.store.session.UserID != 1 || fixture.store.loginOperation != audit.OperationOIDCLoginSucceeded {
		t.Fatalf("callback side effects missing: store=%#v client=%#v", fixture.store, fixture.client)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 || !strings.HasPrefix(cookies[0].Name, sso.OIDCLoginCookieName+"_") || cookies[0].MaxAge != -1 ||
		cookies[1].Name != authn.SessionCookieName || cookies[1].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies = %#v", cookies)
	}
	for _, secret := range []string{"id-token-secret", "access-token-secret"} {
		if strings.Contains(recorder.Body.String()+recorder.Header().Get("Location")+recorder.Header().Get("Set-Cookie"), secret) {
			t.Fatalf("response leaked %q", secret)
		}
	}
	assertPrivateHeaders(t, recorder)
}

func TestOIDCProviderCallbackRejectsMalformedAndBoundRequests(t *testing.T) {
	tests := []struct {
		name, query, cookie string
	}{
		{"missing state", "?code=x", "browser"},
		{"empty state", "?state=&code=x", "browser"},
		{"duplicate state", "?state=x&state=y&code=x", "browser"},
		{"missing result", "?state=x", "browser"},
		{"empty code", "?state=x&code=", "browser"},
		{"duplicate code", "?state=x&code=a&code=b", "browser"},
		{"empty error", "?state=x&error=", "browser"},
		{"duplicate error", "?state=x&error=a&error=b", "browser"},
		{"code and error", "?state=x&code=a&error=b", "browser"},
		{"code with empty error", "?state=x&code=a&error=", "browser"},
		{"error with empty code", "?state=x&code=&error=access_denied", "browser"},
		{"missing cookie", "?state=x&code=a", ""},
		{"malformed cookie", "?state=x&code=a", "malformed"},
		{"wrong cookie", "?state=x&code=a", token(9)},
		{"wrong state", "?state=" + token(8) + "&code=a", "browser"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCallbackFixture(t)
			query := strings.ReplaceAll(test.query, "state=x", "state="+fixture.state)
			cookie := test.cookie
			if cookie == "browser" {
				cookie = fixture.browser
			}
			recorder := fixture.callback(t, query, cookie)
			assertGenericFailure(t, recorder)
			if recorder.Code != http.StatusSeeOther {
				t.Fatalf("status = %d", recorder.Code)
			}
			if (test.name == "wrong cookie" || test.name == "code with empty error" || test.name == "error with empty code") && fixture.store.consumed {
				t.Fatal("invalid callback consumed flow")
			}
		})
	}
}

func TestOIDCProviderCallbackBoundsValuesBeforeConsumingFlow(t *testing.T) {
	for _, test := range []struct {
		name, nameValue    string
		wantConsumed, okay bool
	}{
		{"exact code", "code=" + strings.Repeat("c", maxCallbackValueLen), true, true},
		{"long code", "code=" + strings.Repeat("c", maxCallbackValueLen+1), false, false},
		{"exact error", "error=" + strings.Repeat("e", maxCallbackValueLen), true, false},
		{"long error", "error=" + strings.Repeat("e", maxCallbackValueLen+1), false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCallbackFixture(t)
			recorder := fixture.callback(t, "?state="+fixture.state+"&"+test.nameValue, fixture.browser)
			if test.okay && recorder.Header().Get("Location") != "/" {
				t.Fatalf("exact code response = %q", recorder.Header().Get("Location"))
			}
			if test.wantConsumed && !test.okay {
				assertGenericFailure(t, recorder)
			}
			if fixture.store.consumed != test.wantConsumed {
				t.Fatalf("consumed = %t", fixture.store.consumed)
			}
		})
	}
}

func TestExactlyOneBoundsCallbackValues(t *testing.T) {
	if _, ok := exactlyOne([]string{strings.Repeat("x", maxCallbackValueLen)}); !ok {
		t.Fatal("exact boundary rejected")
	}
	if _, ok := exactlyOne([]string{strings.Repeat("x", maxCallbackValueLen+1)}); ok {
		t.Fatal("over-boundary value accepted")
	}
}

func TestOIDCProviderCallbackRejectsDuplicateBindingCookie(t *testing.T) {
	fixture := newCallbackFixture(t)
	mux := http.NewServeMux()
	fixture.provider.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?state="+fixture.state+"&code=good", nil)
	cookieName := loginCookieName(sso.OIDCLoginCookieName, fixture.store.flow.StateHash)
	request.Header.Add("Cookie", cookieName+"="+fixture.browser)
	request.Header.Add("Cookie", cookieName+"="+fixture.browser)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	assertGenericFailure(t, recorder)
	if fixture.store.consumed {
		t.Fatal("duplicate browser cookie consumed flow")
	}
}

func TestOIDCProviderCallbackConsumesBeforeTerminalFailures(t *testing.T) {
	tests := []struct {
		name, suffix string
		setup        func(*callbackFixture)
	}{
		{"OAuth error", "&error=access_denied&error_description=id-token-secret", nil},
		{"exchange error", "&code=bad", func(f *callbackFixture) { f.client.err = errors.New("access-token-secret") }},
		{"session error", "&code=good", func(f *callbackFixture) { f.store.sessionErr = errors.New("database unavailable") }},
		{"expired", "&code=good", func(f *callbackFixture) { f.provider.Now = func() time.Time { return time.Unix(3_000, 0) } }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCallbackFixture(t)
			if test.setup != nil {
				test.setup(fixture)
			}
			recorder := fixture.callback(t, "?state="+fixture.state+test.suffix, fixture.browser)
			assertGenericFailure(t, recorder)
			if test.name != "expired" && !fixture.store.consumed {
				t.Fatal("flow was not consumed")
			}
			if fixture.store.session.TokenHash != ([32]byte{}) {
				t.Fatal("failure created session")
			}
		})
	}
}

func TestProviderCallbackRejectsWrongIdentityProvider(t *testing.T) {
	fixture := newCallbackFixture(t)
	fixture.client.identity.Provider = authn.ProviderOAuth
	recorder := fixture.callback(t, "?state="+fixture.state+"&code=good", fixture.browser)
	assertGenericFailure(t, recorder)
	if !fixture.store.consumed || fixture.store.session.TokenHash != ([32]byte{}) {
		t.Fatalf("consumed=%t session=%#v", fixture.store.consumed, fixture.store.session)
	}
}

func TestProviderCallbackRejectsWrongFlowProvider(t *testing.T) {
	fixture := newCallbackFixture(t)
	fixture.store.flow.Provider = "wrong"
	recorder := fixture.callback(t, "?state="+fixture.state+"&code=good", fixture.browser)
	assertGenericFailure(t, recorder)
	if fixture.store.consumed || fixture.store.session.TokenHash != ([32]byte{}) {
		t.Fatalf("consumed=%t session=%#v", fixture.store.consumed, fixture.store.session)
	}
}

func TestOIDCProviderCallbackRejectsReplayAndIgnoresReturnTargets(t *testing.T) {
	fixture := newCallbackFixture(t)
	first := fixture.callback(t, "?state="+fixture.state+"&code=good&return_to=https://evil.test", fixture.browser)
	if first.Header().Get("Location") != "/" {
		t.Fatalf("success redirect = %q", first.Header().Get("Location"))
	}
	second := fixture.callback(t, "?state="+fixture.state+"&code=good", fixture.browser)
	assertGenericFailure(t, second)
}

func TestOIDCProviderEnforcesGETMethods(t *testing.T) {
	provider := &Provider{Spec: oidcSpec}
	mux := http.NewServeMux()
	provider.Register(mux)
	for _, path := range []string{"/auth/oidc/login", "/auth/oidc/callback"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
			t.Errorf("POST %s = %d, Allow=%q", path, recorder.Code, recorder.Header().Get("Allow"))
		}
		assertPrivateHeaders(t, recorder)
	}
}

type callbackFixture struct {
	provider *Provider
	client   *providerClient
	store    *providerStore
	state    string
	browser  string
}

func newCallbackFixture(t *testing.T) *callbackFixture {
	t.Helper()
	now := time.Unix(2_000, 0)
	state, browser := token(1), token(2)
	stateRaw, _ := base64.RawURLEncoding.DecodeString(state)
	browserRaw, _ := base64.RawURLEncoding.DecodeString(browser)
	store := &providerStore{flow: authn.LoginFlow{
		StateHash: sha256.Sum256(stateRaw), BrowserHash: sha256.Sum256(browserRaw), Provider: "oidc",
		Nonce: "nonce", CodeVerifier: "verifier", ReturnTo: "/", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}}
	client := &providerClient{identity: authn.Identity{
		Provider: "oidc", Issuer: "https://issuer.example.test", Subject: "ada", LinkID: "directory-42", DisplayName: "Ada",
	}}
	provider := &Provider{
		Spec:   oidcSpec,
		Client: client, Store: store,
		Sessions: &authn.SessionManager{Store: store, IdleTTL: time.Minute, TTL: time.Hour, Now: func() time.Time { return now }, Rand: bytes.NewReader(bytes.Repeat([]byte{4}, 32))},
		Now:      func() time.Time { return now },
	}
	return &callbackFixture{provider: provider, client: client, store: store, state: state, browser: browser}
}

func (fixture *callbackFixture) callback(t *testing.T, query, browser string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	fixture.provider.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback"+query, nil)
	if browser != "" {
		request.AddCookie(&http.Cookie{Name: loginCookieName(fixture.provider.Spec.CookieName, fixture.store.flow.StateHash), Value: browser})
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func token(value byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
}

func assertGenericFailure(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cookies := recorder.Result().Cookies()
	if recorder.Header().Get("Location") != "/?auth_error=authentication_failed" ||
		strings.Contains(recorder.Body.String()+recorder.Header().Get("Location"), "access-token-secret") || len(cookies) > 1 ||
		len(cookies) == 1 && (!strings.HasPrefix(cookies[0].Name, sso.OIDCLoginCookieName+"_") || cookies[0].MaxAge != -1) {
		t.Fatalf("non-generic failure: status=%d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	assertPrivateHeaders(t, recorder)
}

func assertPrivateHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("privacy headers = %v", recorder.Header())
	}
}

func TestLoginHonoursAllowListedReturnTo(t *testing.T) {
	now := time.Unix(1_000, 0)
	newProvider := func(store *providerStore) *Provider {
		return &Provider{
			Spec:   oidcSpec,
			Client: &providerClient{}, Store: store, LoginTTL: 10 * time.Minute, Now: func() time.Time { return now },
			Rand: bytes.NewReader(bytes.Repeat([]byte{1}, 96)), Sessions: &authn.SessionManager{Store: store, TTL: time.Hour},
		}
	}
	cases := map[string]struct {
		query, want string
	}{
		"default":         {"", "/"},
		"oauth resume":    {"?return_to=%2Foauth%2Fauthorize%2Fresume", "/oauth/authorize/resume"},
		"bound resume":    {"?return_to=%2Foauth%2Fauthorize%2Fresume%3Frequest_id%3D" + strings.Repeat("a", 64), "/oauth/authorize/resume?request_id=" + strings.Repeat("a", 64)},
		"open redirect":   {"?return_to=https%3A%2F%2Fevil.test", "/"},
		"relative escape": {"?return_to=%2Fevil", "/"},
		"uppercase ID":    {"?return_to=%2Foauth%2Fauthorize%2Fresume%3Frequest_id%3D" + strings.Repeat("A", 64), "/"},
		"extra parameter": {"?return_to=%2Foauth%2Fauthorize%2Fresume%3Frequest_id%3D" + strings.Repeat("a", 64) + "%26next%3D%2F", "/"},
		"fragment":        {"?return_to=%2Foauth%2Fauthorize%2Fresume%3Frequest_id%3D" + strings.Repeat("a", 64) + "%23x", "/"},
		"encoded path":    {"?return_to=%2Foauth%2F%2561uthorize%2Fresume%3Frequest_id%3D" + strings.Repeat("a", 64), "/"},
		"path suffix":     {"?return_to=%2Foauth%2Fauthorize%2Fresume%2Fx%3Frequest_id%3D" + strings.Repeat("a", 64), "/"},
		"repeated":        {"?return_to=%2Foauth%2Fauthorize%2Fresume&return_to=%2Foauth%2Fauthorize%2Fresume", "/"},
	}
	for name, tc := range cases {
		store := &providerStore{}
		mux := http.NewServeMux()
		newProvider(store).Register(mux)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/oidc/login"+tc.query, nil))
		if recorder.Code != http.StatusSeeOther || store.flow.ReturnTo != tc.want {
			t.Errorf("%s: status=%d return_to=%q want %q", name, recorder.Code, store.flow.ReturnTo, tc.want)
		}
	}
}

func TestCallbackRedirectsToStoredReturnTo(t *testing.T) {
	fixture := newCallbackFixture(t)
	fixture.store.flow.ReturnTo = "/oauth/authorize/resume"
	recorder := fixture.callback(t, "?state="+fixture.state+"&code=good", fixture.browser)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/oauth/authorize/resume" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	// A stored value outside the allow list (tampered database) falls back to /.
	fixture = newCallbackFixture(t)
	fixture.store.flow.ReturnTo = "https://evil.test"
	recorder = fixture.callback(t, "?state="+fixture.state+"&code=good", fixture.browser)
	if recorder.Header().Get("Location") != "/" {
		t.Fatalf("tampered return_to followed: %q", recorder.Header().Get("Location"))
	}
}

type capturingTokens struct {
	requestHash [32]byte
	subject     string
	token       string
}

func (c *capturingTokens) Deposit(_ context.Context, requestHash [32]byte, subject, providerToken string) {
	c.requestHash, c.subject, c.token = requestHash, subject, providerToken
}

func TestCallbackHandsProviderTokenToSinkOnlyForOAuthResume(t *testing.T) {
	for _, tc := range []struct {
		returnTo string
		want     string
	}{{"/oauth/authorize/resume?request_id=" + strings.Repeat("a", 64), "gho_secret"}, {"/", ""}} {
		fixture := newCallbackFixture(t)
		fixture.store.flow.ReturnTo = tc.returnTo
		fixture.client.identity.ProviderToken = "gho_secret"
		sink := &capturingTokens{}
		fixture.provider.Tokens = sink
		recorder := fixture.callback(t, "?state="+fixture.state+"&code=good", fixture.browser)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("status=%d", recorder.Code)
		}
		if sink.token != tc.want || tc.want != "" && (sink.subject != "1" || sink.requestHash != [32]byte{0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa}) {
			t.Fatalf("return_to=%s: sink=%+v want token %q bound to request owner", tc.returnTo, sink, tc.want)
		}
		if strings.Contains(recorder.Header().Get("Set-Cookie")+recorder.Header().Get("Location"), "gho_secret") {
			t.Fatal("provider token leaked to the browser")
		}
	}
}

func TestCallbackCannotResumeWhenFreshSessionCannotBeResolved(t *testing.T) {
	fixture := newCallbackFixture(t)
	fixture.store.flow.ReturnTo = "/oauth/authorize/resume?request_id=" + strings.Repeat("a", 64)
	fixture.store.principalErr = errors.New("database unavailable")
	fixture.client.identity.ProviderToken = "gho_secret"
	sink := &capturingTokens{}
	fixture.provider.Tokens = sink
	recorder := fixture.callback(t, "?state="+fixture.state+"&code=good", fixture.browser)
	if recorder.Header().Get("Location") != "/?auth_error=authentication_failed" || sink.token != "" {
		t.Fatalf("response=%q sink=%+v", recorder.Header().Get("Location"), sink)
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == authn.SessionCookieName {
			t.Fatal("unresolved session was reported to the browser")
		}
	}
}

func TestConcurrentCallbacksSelectAndClearOnlyTheirStateCookies(t *testing.T) {
	first := newCallbackFixture(t)
	second := newCallbackFixture(t)
	second.state, second.browser = token(3), token(4)
	stateRaw, _ := base64.RawURLEncoding.DecodeString(second.state)
	browserRaw, _ := base64.RawURLEncoding.DecodeString(second.browser)
	second.store.flow.StateHash = sha256.Sum256(stateRaw)
	second.store.flow.BrowserHash = sha256.Sum256(browserRaw)
	second.provider.Sessions.Rand = bytes.NewReader(bytes.Repeat([]byte{5}, 32))
	first.store.flow.ReturnTo = "/oauth/authorize/resume?request_id=" + strings.Repeat("a", 64)
	second.store.flow.ReturnTo = "/oauth/authorize/resume?request_id=" + strings.Repeat("b", 64)
	first.client.identity.ProviderToken = "first-token"
	second.client.identity.ProviderToken = "second-token"
	tokens := oauthas.NewProviderTokens(func() time.Time { return time.Unix(2_000, 0) })
	first.provider.Tokens, second.provider.Tokens = tokens, tokens
	firstCookie := loginCookieName(first.provider.Spec.CookieName, first.store.flow.StateHash)
	secondCookie := loginCookieName(second.provider.Spec.CookieName, second.store.flow.StateHash)

	callback := func(fixture *callbackFixture, state string) *httptest.ResponseRecorder {
		t.Helper()
		mux := http.NewServeMux()
		fixture.provider.Register(mux)
		request := httptest.NewRequest(http.MethodGet, fixture.provider.Spec.CallbackPath+"?state="+state+"&code=good", nil)
		request.AddCookie(&http.Cookie{Name: firstCookie, Value: first.browser})
		request.AddCookie(&http.Cookie{Name: secondCookie, Value: second.browser})
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	firstResponse := callback(first, first.state)
	secondResponse := callback(second, second.state)
	if firstResponse.Header().Get("Set-Cookie") == "" || !strings.HasPrefix(firstResponse.Result().Cookies()[0].Name, firstCookie) ||
		secondResponse.Header().Get("Set-Cookie") == "" || !strings.HasPrefix(secondResponse.Result().Cookies()[0].Name, secondCookie) {
		t.Fatalf("callback cookies first=%#v second=%#v", firstResponse.Result().Cookies(), secondResponse.Result().Cookies())
	}
	firstRequest, _ := oauthResumeRequest(first.store.flow.ReturnTo)
	secondRequest, _ := oauthResumeRequest(second.store.flow.ReturnTo)
	firstCode, secondCode := sha256.Sum256([]byte("first-code")), sha256.Sum256([]byte("second-code"))
	if !first.store.consumed || !second.store.consumed || !tokens.Transfer(firstRequest, "1", firstCode) || !tokens.Transfer(secondRequest, "1", secondCode) {
		t.Fatal("callbacks did not preserve both request-owned handoffs")
	}
	firstToken, firstOK := tokens.TokenForCode(firstCode)
	secondToken, secondOK := tokens.TokenForCode(secondCode)
	if !firstOK || !secondOK || firstToken != "first-token" || secondToken != "second-token" {
		t.Fatalf("callback handoffs first=%q/%t second=%q/%t", firstToken, firstOK, secondToken, secondOK)
	}
}
