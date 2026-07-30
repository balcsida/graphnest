package admin

import (
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
)

func TestAuditEventsRequireDurableIdentityAdministrator(t *testing.T) {
	service := Service{Store: &identityStore{}}
	for _, principal := range []authn.Principal{
		{Subject: "7", Method: "api_token", Administrator: true},
		{Subject: "static", Method: "static", Administrator: true},
		{Subject: "7", Method: "oidc"},
	} {
		if _, _, err := service.AuditEvents(t.Context(), principal); err != ErrForbidden {
			t.Fatalf("principal=%#v error=%v", principal, err)
		}
	}
	if _, _, err := service.AuditEvents(t.Context(), authn.Principal{
		Subject: "7", Method: "oidc", Administrator: true,
	}); err != nil {
		t.Fatal(err)
	}
}
