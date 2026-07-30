package authn

import (
	"strings"
	"testing"
)

func TestProvisioningAuthenticator(t *testing.T) {
	secret := strings.Repeat("s", 32)
	authenticator, err := NewProvisioningAuthenticator([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		values []string
		valid  bool
	}{
		{"missing", nil, false},
		{"empty", []string{""}, false},
		{"wrong scheme", []string{"Basic " + secret}, false},
		{"missing value", []string{"Bearer"}, false},
		{"lowercase scheme", []string{"bearer " + secret}, false},
		{"extra whitespace", []string{"Bearer  " + secret}, false},
		{"trailing whitespace", []string{"Bearer " + secret + " "}, false},
		{"comma ambiguity", []string{"Bearer " + secret + ",Bearer " + secret}, false},
		{"duplicate", []string{"Bearer " + secret, "Bearer " + secret}, false},
		{"invalid", []string{"Bearer " + strings.Repeat("x", 32)}, false},
		{"valid", []string{"Bearer " + secret}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := authenticator.Authenticate(test.values); (err == nil) != test.valid {
				t.Fatalf("Authenticate() error = %v, valid=%t", err, test.valid)
			}
		})
	}
}

func TestProvisioningAuthenticatorRejectsShortSecrets(t *testing.T) {
	if _, err := NewProvisioningAuthenticator([]byte(strings.Repeat("s", 31))); err == nil {
		t.Fatal("short secret accepted")
	}
}
