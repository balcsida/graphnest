package authn

import "testing"

func TestIdentityRequiresLinkID(t *testing.T) {
	if validIdentity(Identity{Provider: "oidc", Issuer: "https://issuer.example.test", Subject: "subject"}) {
		t.Fatal("identity without immutable link ID was accepted")
	}
}
