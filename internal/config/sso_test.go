package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadOIDCRequiresCompleteDurableConfiguration(t *testing.T) {
	for _, name := range []string{"GREPNEST_OIDC_ISSUER_URL", "GREPNEST_OIDC_CLIENT_ID", "GREPNEST_OIDC_CLIENT_SECRET_FILE"} {
		t.Run("missing "+name, func(t *testing.T) {
			setValidEnvironment(t)
			setDurableEnvironment(t)
			t.Setenv("GREPNEST_OIDC_ISSUER_URL", "https://idp.example.test/realms/engineering")
			t.Setenv("GREPNEST_OIDC_CLIENT_ID", "grepnest")
			t.Setenv("GREPNEST_OIDC_CLIENT_SECRET_FILE", "/run/secrets/oidc-client-secret")
			t.Setenv(name, "")
			if _, err := Load(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), name) {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}

	setValidEnvironment(t)
	t.Setenv("GREPNEST_OIDC_ISSUER_URL", "https://idp.example.test/realms/engineering")
	t.Setenv("GREPNEST_OIDC_CLIENT_ID", "grepnest")
	t.Setenv("GREPNEST_OIDC_CLIENT_SECRET_FILE", "/run/secrets/oidc-client-secret")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GREPNEST_DATABASE_URL") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadOIDCRequiresDurableHTTPSOrigin(t *testing.T) {
	setValidOIDCEnvironment(t)
	for _, value := range []string{"", "http://grepnest.example.test", "https://user@grepnest.example.test", "https://grepnest.example.test/path", "https://grepnest.example.test/?query=1", "https://grepnest.example.test/#fragment"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GREPNEST_PUBLIC_URL", value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GREPNEST_PUBLIC_URL") {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestLoadOIDCValidatesIssuer(t *testing.T) {
	for _, value := range []string{"http://idp.example.test", "https://idp.example.test?query=1", "https://idp.example.test#fragment"} {
		t.Run(value, func(t *testing.T) {
			setValidOIDCEnvironment(t)
			t.Setenv("GREPNEST_OIDC_ISSUER_URL", value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GREPNEST_OIDC_ISSUER_URL") {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestLoadOIDCPreservesIssuerPathAndDefaults(t *testing.T) {
	setValidOIDCEnvironment(t)
	t.Setenv("GREPNEST_OIDC_ISSUER_URL", "https://idp.example.test/realms/engineering/")

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SSO.PublicURL.String() != "https://grepnest.example.test/" || got.SSO.SessionTTL != 8*time.Hour || got.SSO.LoginFlowTTL != 10*time.Minute || got.SSO.OIDC.IssuerURL != "https://idp.example.test/realms/engineering/" || !reflect.DeepEqual(got.SSO.OIDC.Scopes, []string{"openid", "profile", "email"}) || got.SSO.OIDC.GroupsClaim != "groups" || got.SSO.OIDC.DisplayNameClaim != "name" || got.SSO.OIDC.AllowedGroups != nil {
		t.Fatalf("SSO = %#v", got.SSO)
	}
}

func TestLoadOIDCValidatesScopes(t *testing.T) {
	for _, test := range []struct {
		name, scopes string
		want         []string
		err          bool
	}{
		{"deduplicates and trims", " openid, profile,openid, email ", []string{"openid", "profile", "email"}, false},
		{"rejects empty", "openid,,profile", nil, true},
		{"requires openid", "profile,email", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidOIDCEnvironment(t)
			t.Setenv("GREPNEST_OIDC_SCOPES", test.scopes)
			got, err := Load()
			if test.err {
				if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "GREPNEST_OIDC_SCOPES") {
					t.Fatalf("Load() error = %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got.SSO.OIDC.Scopes, test.want) {
				t.Fatalf("Load() = %#v, %v", got.SSO.OIDC.Scopes, err)
			}
		})
	}
}

func TestLoadOIDCValidatesTTLs(t *testing.T) {
	for _, test := range []struct {
		name, variable, value string
		want                  time.Duration
		err                   bool
	}{
		{"session minimum", "GREPNEST_SSO_SESSION_TTL", "5m", 5 * time.Minute, false},
		{"session below minimum", "GREPNEST_SSO_SESSION_TTL", "4m", 0, true},
		{"session maximum", "GREPNEST_SSO_SESSION_TTL", "24h", 24 * time.Hour, false},
		{"session above maximum", "GREPNEST_SSO_SESSION_TTL", "25h", 0, true},
		{"login minimum", "GREPNEST_SSO_LOGIN_FLOW_TTL", "1m", time.Minute, false},
		{"login below minimum", "GREPNEST_SSO_LOGIN_FLOW_TTL", "59s", 0, true},
		{"login maximum", "GREPNEST_SSO_LOGIN_FLOW_TTL", "15m", 15 * time.Minute, false},
		{"login above maximum", "GREPNEST_SSO_LOGIN_FLOW_TTL", "16m", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidOIDCEnvironment(t)
			t.Setenv(test.variable, test.value)
			got, err := Load()
			if test.err {
				if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), test.variable) {
					t.Fatalf("Load() error = %v", err)
				}
				return
			}
			if err != nil || (test.variable == "GREPNEST_SSO_SESSION_TTL" && got.SSO.SessionTTL != test.want) || (test.variable == "GREPNEST_SSO_LOGIN_FLOW_TTL" && got.SSO.LoginFlowTTL != test.want) {
				t.Fatalf("Load() = %#v, %v", got.SSO, err)
			}
		})
	}
}

func TestLoadOIDCTrimsAllowedGroups(t *testing.T) {
	setValidOIDCEnvironment(t)
	t.Setenv("GREPNEST_OIDC_ALLOWED_GROUPS", " engineering, admins ")

	got, err := Load()
	if err != nil || !reflect.DeepEqual(got.SSO.OIDC.AllowedGroups, []string{"engineering", "admins"}) {
		t.Fatalf("Load() = %#v, %v", got.SSO.OIDC.AllowedGroups, err)
	}
}

func setValidOIDCEnvironment(t *testing.T) {
	t.Helper()
	setValidEnvironment(t)
	setDurableEnvironment(t)
	t.Setenv("GREPNEST_PUBLIC_URL", "https://grepnest.example.test")
	t.Setenv("GREPNEST_OIDC_ISSUER_URL", "https://idp.example.test/realms/engineering")
	t.Setenv("GREPNEST_OIDC_CLIENT_ID", "grepnest")
	t.Setenv("GREPNEST_OIDC_CLIENT_SECRET_FILE", "/run/secrets/oidc-client-secret")
}
