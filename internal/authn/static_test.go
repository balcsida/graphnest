package authn

import (
	"context"
	"testing"
)

func TestAuthenticateReturnsDefensiveDurableRepositoryIDs(t *testing.T) {
	auth := NewStatic(map[string]Principal{"token": {InstallationID: 10, RepositoryIDs: []int64{101}}})
	got, err := auth.Authenticate(context.Background(), "token")
	if err != nil || got.InstallationID != 10 || len(got.RepositoryIDs) != 1 || got.RepositoryIDs[0] != 101 {
		t.Fatalf("got %#v, %v", got, err)
	}
	got.RepositoryIDs[0] = 999
	again, err := auth.Authenticate(context.Background(), "token")
	if err != nil || again.RepositoryIDs[0] != 101 {
		t.Fatalf("got %#v, %v", again, err)
	}
}

func TestAuthenticateUsesOpaqueTokenLengths(t *testing.T) {
	auth := NewStatic(map[string]Principal{"short": {Subject: "user"}})
	got, err := auth.Authenticate(context.Background(), "short")
	if err != nil || got.Subject != "user" {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestAuthenticateRejectsUnknownToken(t *testing.T) {
	auth := NewStatic(map[string]Principal{"short": {Subject: "user"}})
	if _, err := auth.Authenticate(context.Background(), "longer-token"); err != ErrUnauthenticated {
		t.Fatalf("Authenticate() error = %v", err)
	}
}
