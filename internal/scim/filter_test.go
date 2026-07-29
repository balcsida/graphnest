package scim

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseFilter(t *testing.T) {
	for _, test := range []struct {
		name, raw, attribute, value string
		resource                    ResourceType
		wantErr                     string
	}{
		{"user names are case insensitive", `UsErNaMe EQ "ada@example.test"`, "userName", "ada@example.test", ResourceUsers, ""},
		{"escaped value", `externalId eq "directory\u002d42"`, "externalId", "directory-42", ResourceUsers, ""},
		{"user value with spaces", `externalId eq "Directory User 42"`, "externalId", "Directory User 42", ResourceUsers, ""},
		{"group display name", `DISPLAYNAME eq "Engineering"`, "displayName", "Engineering", ResourceGroups, ""},
		{"group display name with spaces", `displayName eq "Engineering Team"`, "displayName", "Engineering Team", ResourceGroups, ""},
		{"logical expression", `userName eq "ada" and active eq true`, "", "", ResourceUsers, "invalidFilter"},
		{"trailing junk", `userName eq "ada" junk`, "", "", ResourceUsers, "invalidFilter"},
		{"unapproved attribute", `active eq "true"`, "", "", ResourceUsers, "invalidFilter"},
		{"oversized", strings.Repeat("a", 4097), "", "", ResourceUsers, "invalidFilter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseFilter(test.resource, test.raw)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil || got.Attribute != test.attribute || got.Value != test.value {
				t.Fatalf("filter=%#v err=%v", got, err)
			}
		})
	}
}

func TestParsePage(t *testing.T) {
	for _, test := range []struct {
		name    string
		values  url.Values
		want    Page
		wantErr bool
	}{
		{"defaults", url.Values{}, Page{StartIndex: 1, Count: 10}, false},
		{"one based zero count", url.Values{"startIndex": {"1"}, "count": {"0"}}, Page{StartIndex: 1, Count: 0}, false},
		{"clamps count", url.Values{"startIndex": {"3"}, "count": {"99"}}, Page{StartIndex: 3, Count: 10}, false},
		{"rejects duplicates", url.Values{"count": {"1", "2"}}, Page{}, true},
		{"rejects zero index", url.Values{"startIndex": {"0"}}, Page{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParsePage(test.values, 10)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("page=%#v err=%v", got, err)
			}
		})
	}
}
