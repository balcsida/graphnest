package authn

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestScopeMapperMapsAllowedIdentity(t *testing.T) {
	mapper := ScopeMapper{InstallationID: 10, RepositoryIDs: []int64{102, 101, 102}, AllowedGroups: []string{"engineers"}}
	got, err := mapper.Map(Identity{Provider: "oidc", Issuer: "https://idp.example", Subject: "ada", DisplayName: "Ada", Groups: []string{"engineers"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Subject, "oidc:") || strings.Contains(got.Subject, "ada") || strings.Contains(got.Subject, "idp.example") || got.Method != "oidc" || got.DisplayName != "Ada" || got.Administrator || got.InstallationID != 10 || !reflect.DeepEqual(got.RepositoryIDs, []int64{101, 102}) {
		t.Fatalf("principal = %#v", got)
	}
}

func TestScopeMapperRejectsInvalidIdentity(t *testing.T) {
	for name, identity := range map[string]Identity{
		"missing provider": {Issuer: "issuer", Subject: "subject"},
		"missing issuer":   {Provider: "oidc", Subject: "subject"},
		"missing subject":  {Provider: "oidc", Issuer: "issuer"},
		"long issuer":      {Provider: "oidc", Issuer: strings.Repeat("a", 2049), Subject: "subject"},
		"long subject":     {Provider: "oidc", Issuer: "issuer", Subject: strings.Repeat("a", 1025)},
		"long name":        {Provider: "oidc", Issuer: "issuer", Subject: "subject", DisplayName: strings.Repeat("a", 257)},
		"many groups":      {Provider: "oidc", Issuer: "issuer", Subject: "subject", Groups: make([]string, 257)},
		"long group":       {Provider: "oidc", Issuer: "issuer", Subject: "subject", Groups: []string{strings.Repeat("a", 257)}},
		"large groups":     {Provider: "oidc", Issuer: "issuer", Subject: "subject", Groups: repeatedGroups(129, strings.Repeat("a", 256))},
		"bad utf8":         {Provider: "oidc", Issuer: string([]byte{0xff}), Subject: "subject"},
		"empty group":      {Provider: "oidc", Issuer: "issuer", Subject: "subject", Groups: []string{""}},
		"control group":    {Provider: "oidc", Issuer: "issuer", Subject: "subject", Groups: []string{"ops\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (ScopeMapper{}).Map(identity)
			if !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func repeatedGroups(n int, group string) []string {
	groups := make([]string, n)
	for i := range groups {
		groups[i] = group
	}
	return groups
}

func TestScopeMapperRequiresExactAllowedGroup(t *testing.T) {
	mapper := ScopeMapper{AllowedGroups: []string{"Engineers"}}
	if _, err := mapper.Map(Identity{Provider: "oidc", Issuer: "issuer", Subject: "subject", Groups: []string{"engineers"}}); !errors.Is(err, ErrIdentityForbidden) {
		t.Fatalf("error = %v", err)
	}
	if _, err := (ScopeMapper{}).Map(Identity{Provider: "oidc", Issuer: "issuer", Subject: "subject"}); err != nil {
		t.Fatal(err)
	}
}
