package oauthas

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

var consentRequestID = regexp.MustCompile(`name="request_id" value="([^"]+)"`)

func TestConcurrentAuthorizationsUseIndependentRequestCookies(t *testing.T) {
	h := newHarness(t)
	h.clock = time.Now().UTC()
	h.server.GitHub = nil
	h.server.GitHubTokens = nil
	tls := httptest.NewTLSServer(h.mux)
	t.Cleanup(tls.Close)
	h.server.Origin = tls.URL

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client()
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	originURL, _ := url.Parse(tls.URL)
	jar.SetCookies(originURL, []*http.Cookie{{Name: authn.SessionCookieName, Value: sessionTokenValue, Path: "/", Secure: true}})

	start := func(id, redirect, state string) string {
		t.Helper()
		h.store.clients[id] = authn.OAuthClient{ID: id, Name: id, RedirectURIs: []string{redirect}, CreatedAt: h.clock}
		_, challenge := pkce()
		query := url.Values{
			"response_type": {"code"}, "client_id": {id}, "redirect_uri": {redirect},
			"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state},
		}
		response, err := client.Get(tls.URL + "/oauth/authorize?" + query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		match := consentRequestID.FindSubmatch(body)
		if response.StatusCode != http.StatusOK || len(match) != 2 {
			t.Fatalf("start %s: status=%d body=%s", id, response.StatusCode, body)
		}
		requestID := string(match[1])
		return requestID
	}
	decide := func(requestID, decision string) *http.Response {
		t.Helper()
		form := url.Values{"request_id": {requestID}, "decision": {decision}}
		request, err := http.NewRequest(http.MethodPost, tls.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", tls.URL)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	aID := start("gnc_a", "http://127.0.0.1:5001/callback", "state-a")
	bID := start("gnc_b", "http://127.0.0.1:5002/callback", "state-b")
	if aID == bID {
		t.Fatal("concurrent authorizations reused a request ID")
	}

	bResponse := decide(bID, "deny")
	bResponse.Body.Close()
	if location := bResponse.Header.Get("Location"); bResponse.StatusCode != http.StatusSeeOther || !strings.Contains(location, "state=state-b") {
		t.Fatalf("deny B: status=%d location=%q", bResponse.StatusCode, location)
	}
	aPreservedAfterB := hasCookie(jar.Cookies(originURL), RequestCookie+"_"+aID)
	bClearedAfterB := !hasCookie(jar.Cookies(originURL), RequestCookie+"_"+bID)

	aResponse := decide(aID, "allow")
	aResponse.Body.Close()
	if location := aResponse.Header.Get("Location"); aResponse.StatusCode != http.StatusSeeOther || !strings.Contains(location, "state=state-a") {
		t.Fatalf("allow A: status=%d location=%q", aResponse.StatusCode, location)
	}
	if !consentRequestID.MatchString(`name="request_id" value="`+aID+`"`) || len(aID) != 64 || len(bID) != 64 {
		t.Fatalf("request IDs are not lowercase SHA256 hex: %q %q", aID, bID)
	}
	if !aPreservedAfterB || !bClearedAfterB || hasCookie(jar.Cookies(originURL), RequestCookie+"_"+aID) {
		t.Fatalf("completed requests changed wrong cookies: %#v", jar.Cookies(originURL))
	}

	cID := start("gnc_c", "http://127.0.0.1:5003/callback", "state-c")
	dID := start("gnc_d", "http://127.0.0.1:5004/callback", "state-d")
	cResponse := decide(cID, "allow")
	cResponse.Body.Close()
	if location := cResponse.Header.Get("Location"); cResponse.StatusCode != http.StatusSeeOther || !strings.Contains(location, "state=state-c") || !hasCookie(jar.Cookies(originURL), RequestCookie+"_"+dID) {
		t.Fatalf("allow C: status=%d location=%q cookies=%#v", cResponse.StatusCode, location, jar.Cookies(originURL))
	}
	dResponse := decide(dID, "deny")
	dResponse.Body.Close()
	if location := dResponse.Header.Get("Location"); dResponse.StatusCode != http.StatusSeeOther || !strings.Contains(location, "state=state-d") {
		t.Fatalf("deny D: status=%d location=%q", dResponse.StatusCode, location)
	}
}

func hasCookie(cookies []*http.Cookie, name string) bool {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return true
		}
	}
	return false
}

type mappedSessions map[string]authn.Principal

func (sessions mappedSessions) Authenticate(_ context.Context, token string) (authn.Principal, error) {
	principal, ok := sessions[token]
	if !ok {
		return authn.Principal{}, authn.ErrUnauthenticated
	}
	return principal, nil
}

type failingIssueStore struct {
	*memoryStore
	failures int
}

func (store *failingIssueStore) IssueOAuthCode(ctx context.Context, pendingID, codeID, sessionHash [32]byte, userID int64, expiresAt, now time.Time) error {
	if store.failures > 0 {
		store.failures--
		return errors.New("database unavailable")
	}
	return store.memoryStore.IssueOAuthCode(ctx, pendingID, codeID, sessionHash, userID, expiresAt, now)
}

func TestProviderHandoffOwnerMismatchDoesNotConsumeRequest(t *testing.T) {
	h := newHarness(t)
	otherSession := "IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI"
	h.server.Sessions = mappedSessions{
		sessionTokenValue: {Subject: "11", Method: authn.ProviderOAuth},
		otherSession:      {Subject: "12", Method: authn.ProviderOAuth},
	}
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	_, challenge := pkce()
	request := httptest.NewRequest(http.MethodGet, authorizeURL(clientID, "http://127.0.0.1:5000/cb", challenge), nil)
	response := h.do(request)
	requestCookie := cookieNamed(response, RequestCookie)
	requestID := strings.TrimPrefix(requestCookie.Name, RequestCookie+"_")
	_, pendingID, _ := parseRequestID([]string{requestID})
	tokens := h.server.GitHubTokens.(*ProviderTokens)
	tokens.Deposit(t.Context(), pendingID, "11", "first-token")

	post := func(session string) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"request_id": {requestID}, "decision": {"allow"}}
		request := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", origin)
		request.AddCookie(requestCookie)
		request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: session})
		return h.do(request)
	}

	wrong := post(otherSession)
	if wrong.Code != http.StatusSeeOther || !strings.Contains(wrong.Header().Get("Location"), "error=server_error") || !tokens.Available(pendingID, "11") {
		t.Fatalf("wrong owner status=%d location=%q available=%t", wrong.Code, wrong.Header().Get("Location"), tokens.Available(pendingID, "11"))
	}
	if _, ok := h.store.requests[pendingID]; !ok {
		t.Fatal("wrong owner consumed pending request")
	}
	right := post(sessionTokenValue)
	if right.Code != http.StatusSeeOther || !strings.Contains(right.Header().Get("Location"), "code=") {
		t.Fatalf("right owner status=%d location=%q", right.Code, right.Header().Get("Location"))
	}
}

func TestAuthorizationIssuanceFailurePreservesProviderHandoff(t *testing.T) {
	h := newHarness(t)
	h.server.Store = &failingIssueStore{memoryStore: h.store, failures: 1}
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	_, challenge := pkce()
	request := httptest.NewRequest(http.MethodGet, authorizeURL(clientID, "http://127.0.0.1:5000/cb", challenge), nil)
	response := h.do(request)
	requestCookie := cookieNamed(response, RequestCookie)
	requestID := strings.TrimPrefix(requestCookie.Name, RequestCookie+"_")
	_, pendingID, _ := parseRequestID([]string{requestID})
	tokens := h.server.GitHubTokens.(*ProviderTokens)
	tokens.Deposit(t.Context(), pendingID, "11", "retry-token")
	form := url.Values{"request_id": {requestID}, "decision": {"allow"}}
	post := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", origin)
		request.AddCookie(requestCookie)
		request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
		return h.do(request)
	}

	failed := post()
	if failed.Code != http.StatusSeeOther || !strings.Contains(failed.Header().Get("Location"), "error=server_error") || !tokens.Available(pendingID, "11") || cookieNamed(failed, requestCookie.Name) != nil {
		t.Fatalf("failed issuance status=%d location=%q available=%t cookies=%#v", failed.Code, failed.Header().Get("Location"), tokens.Available(pendingID, "11"), failed.Result().Cookies())
	}
	retried := post()
	location, _ := url.Parse(retried.Header().Get("Location"))
	codeHash, ok := hashSecret(location.Query().Get("code"), CodePrefix)
	if retried.Code != http.StatusSeeOther || !ok {
		t.Fatalf("retry status=%d location=%q", retried.Code, retried.Header().Get("Location"))
	}
	if token, found := tokens.TokenForCode(codeHash); !found || token != "retry-token" {
		t.Fatalf("retry handoff token=%q found=%t", token, found)
	}
}

func TestConsentRejectsUnboundRequestIDsAndCookies(t *testing.T) {
	h := newHarness(t)
	h.server.GitHub = nil
	h.server.GitHubTokens = nil
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	_, challenge := pkce()
	start := httptest.NewRequest(http.MethodGet, authorizeURL(clientID, "http://127.0.0.1:5000/cb", challenge), nil)
	start.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
	requestCookie := cookieNamed(h.do(start), RequestCookie)
	requestID := strings.TrimPrefix(requestCookie.Name, RequestCookie+"_")
	for _, test := range []struct {
		name    string
		ids     []string
		cookies []*http.Cookie
	}{
		{"missing ID", nil, []*http.Cookie{requestCookie}},
		{"repeated ID", []string{requestID, requestID}, []*http.Cookie{requestCookie}},
		{"uppercase ID", []string{strings.ToUpper(requestID)}, []*http.Cookie{requestCookie}},
		{"short ID", []string{requestID[:63]}, []*http.Cookie{requestCookie}},
		{"missing cookie", []string{requestID}, nil},
		{"wrong cookie", []string{requestID}, []*http.Cookie{{Name: requestCookie.Name, Value: sessionTokenValue}}},
		{"duplicate cookie", []string{requestID}, []*http.Cookie{requestCookie, requestCookie}},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{"decision": {"allow"}, "request_id": test.ids}
			request := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", origin)
			request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
			for _, cookie := range test.cookies {
				request.AddCookie(cookie)
			}
			response := h.do(request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
			}
		})
	}
	_, pendingID, _ := parseRequestID([]string{requestID})
	if _, ok := h.store.requests[pendingID]; !ok {
		t.Fatal("invalid consent consumed pending request")
	}
}

func TestResumeSelectsExactlyOneBoundRequestCookie(t *testing.T) {
	h := newHarness(t)
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	_, challenge := pkce()
	response := h.do(httptest.NewRequest(http.MethodGet, authorizeURL(clientID, "http://127.0.0.1:5000/cb", challenge), nil))
	requestCookie := cookieNamed(response, RequestCookie)
	requestID := strings.TrimPrefix(requestCookie.Name, RequestCookie+"_")
	for _, target := range []string{
		ResumePath,
		ResumePath + "?request_id=" + requestID + "&request_id=" + requestID,
		ResumePath + "?request_id=" + requestID[:63],
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(requestCookie)
		request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
		if got := h.do(request); got.Code != http.StatusBadRequest {
			t.Fatalf("resume %q status=%d", target, got.Code)
		}
	}
	valid := httptest.NewRequest(http.MethodGet, ResumePath+"?request_id="+requestID, nil)
	valid.AddCookie(requestCookie)
	valid.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
	if got := h.do(valid); got.Code != http.StatusOK {
		t.Fatalf("valid resume status=%d body=%s", got.Code, got.Body.String())
	}
}
