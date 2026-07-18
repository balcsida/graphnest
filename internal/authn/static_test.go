package authn

import "testing"

func TestAuthenticateUsesOpaqueTokenLengths(t *testing.T) {
	auth := NewStatic(map[string]Principal{"short": {Subject: "user"}})
	got, err := auth.Authenticate("short")
	if err != nil || got.Subject != "user" {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestAuthenticateRejectsUnknownToken(t *testing.T) {
	auth := NewStatic(map[string]Principal{"short": {Subject: "user"}})
	if _, err := auth.Authenticate("longer-token"); err != ErrUnauthenticated {
		t.Fatalf("Authenticate() error = %v", err)
	}
}
