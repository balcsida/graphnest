package authn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type requestSession struct {
	principal Principal
	err       error
	calls     int
}

func (s *requestSession) Authenticate(_ context.Context, _ string) (Principal, error) {
	s.calls++
	return s.principal, s.err
}

func TestRequestAuthenticatorSelectsExactlyOneCredential(t *testing.T) {
	tests := []struct {
		name          string
		authorization []string
		cookies       []*http.Cookie
		want          string
		wantSession   int
	}{
		{"missing", nil, nil, "", 0},
		{"valid bearer", []string{"Bearer bearer"}, nil, "bearer", 0},
		{"invalid bearer", []string{"Bearer invalid"}, nil, "", 0},
		{"valid cookie", nil, []*http.Cookie{{Name: SessionCookieName, Value: "session"}}, "session", 1},
		{"invalid cookie", nil, []*http.Cookie{{Name: SessionCookieName, Value: "bad"}}, "", 1},
		{"mixed valid", []string{"Bearer bearer"}, []*http.Cookie{{Name: SessionCookieName, Value: "session"}}, "", 0},
		{"mixed invalid bearer", []string{"Bearer invalid"}, []*http.Cookie{{Name: SessionCookieName, Value: "session"}}, "", 0},
		{"duplicate authorization", []string{"Bearer bearer", "Bearer bearer"}, nil, "", 0},
		{"non bearer", []string{"Basic credential"}, []*http.Cookie{{Name: SessionCookieName, Value: "session"}}, "", 0},
		{"bearer duplicate cookies", []string{"Bearer bearer"}, []*http.Cookie{{Name: SessionCookieName, Value: "session"}, {Name: SessionCookieName, Value: "session"}}, "", 0},
		{"duplicate cookies", nil, []*http.Cookie{{Name: SessionCookieName, Value: "session"}, {Name: SessionCookieName, Value: "session"}}, "", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &requestSession{principal: Principal{Subject: "session"}}
			if test.name == "invalid cookie" {
				session.err = errors.New("missing")
			}
			authenticator := RequestAuthenticator{
				Bearer:  NewStatic(map[string]Principal{"bearer": {Subject: "bearer"}}),
				Session: session,
			}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, value := range test.authorization {
				request.Header.Add("Authorization", value)
			}
			for _, cookie := range test.cookies {
				request.AddCookie(cookie)
			}

			principal, err := authenticator.AuthenticateRequest(request)
			if test.want == "" {
				if !errors.Is(err, ErrUnauthenticated) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || principal.Subject != test.want {
				t.Fatalf("principal = %#v, error = %v", principal, err)
			}
			if session.calls != test.wantSession {
				t.Fatalf("session calls = %d, want %d", session.calls, test.wantSession)
			}
		})
	}
}

func TestRequestAuthenticatorRequiresExactOriginForUnsafeSessionRequests(t *testing.T) {
	for _, test := range []struct {
		name, origin string
		want         bool
	}{
		{"exact", "https://grepnest.example.test", true},
		{"missing", "", false},
		{"null", "null", false},
		{"other scheme", "http://grepnest.example.test", false},
		{"other host", "https://other.example.test", false},
		{"other port", "https://grepnest.example.test:443", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			request.Header.Set("Origin", test.origin)
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
			_, err := (RequestAuthenticator{Session: &requestSession{principal: Principal{Subject: "session"}}, PublicOrigin: "https://grepnest.example.test"}).AuthenticateRequest(request)
			if (err == nil) != test.want {
				t.Fatalf("error = %v, want success=%t", err, test.want)
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer bearer")
	if _, err := (RequestAuthenticator{Bearer: NewStatic(map[string]Principal{"bearer": {Subject: "bearer"}})}).AuthenticateRequest(request); err != nil {
		t.Fatalf("bearer POST error = %v", err)
	}
}
