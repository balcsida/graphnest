package authn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type requestSessionStub struct{ principal Principal }

func (s requestSessionStub) Authenticate(context.Context, string) (Principal, error) {
	return s.principal, nil
}

func TestRequestAuthenticatorRejectsMixedCredentials(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://search.example.test/v1/search", nil)
	request.Header.Set("Authorization", "Bearer token")
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
	_, err := (RequestAuthenticator{Bearer: NewStatic(map[string]Principal{"token": {Subject: "bearer"}}), Session: requestSessionStub{principal: Principal{Subject: "session"}}}).AuthenticateRequest(request)
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err=%v", err)
	}
}

func TestRequestAuthenticatorRejectsDuplicateCredentials(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(*http.Request)
	}{
		{"headers", func(request *http.Request) {
			request.Header.Add("Authorization", "Bearer token")
			request.Header.Add("Authorization", "Bearer token")
		}},
		{"cookies", func(request *http.Request) {
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://search.example.test/v1/search", nil)
			test.set(request)
			_, err := (RequestAuthenticator{Bearer: NewStatic(map[string]Principal{"token": {Subject: "bearer"}}), Session: requestSessionStub{principal: Principal{Subject: "session"}}}).AuthenticateRequest(request)
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRequestAuthenticatorRequiresExactOriginForUnsafeSessionRequests(t *testing.T) {
	for _, test := range []struct {
		origin string
		want   bool
	}{
		{"https://search.example.test", true},
		{"https://search.example.test:443", false},
		{"", false},
	} {
		request := httptest.NewRequest(http.MethodPost, "https://search.example.test/v1/search", nil)
		request.Header.Set("Origin", test.origin)
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
		_, err := (RequestAuthenticator{Session: requestSessionStub{principal: Principal{Subject: "session"}}, PublicOrigin: "https://search.example.test"}).AuthenticateRequest(request)
		if (err == nil) != test.want {
			t.Fatalf("origin=%q err=%v", test.origin, err)
		}
	}
}

func TestRequestAuthenticatorRejectsUnsafeSessionRequestWithoutConfiguredOrigin(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://search.example.test/v1/search", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
	_, err := (RequestAuthenticator{Session: requestSessionStub{principal: Principal{Subject: "session"}}}).AuthenticateRequest(request)
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err=%v", err)
	}
}
