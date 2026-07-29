package scim

import (
	"net/url"
	"testing"
)

func TestParseProjection(t *testing.T) {
	got, err := ParseProjection(url.Values{"attributes": {"userName,displayName"}, "excludedAttributes": {"emails"}}, ResourceUsers)
	if err != nil {
		t.Fatal(err)
	}
	for _, attribute := range []string{"userName", "displayName", "schemas", "id", "meta"} {
		if !got.Include[attribute] {
			t.Fatalf("include=%#v missing %q", got.Include, attribute)
		}
	}
	if !got.Exclude["emails"] || got.Exclude["id"] || got.Exclude["meta"] {
		t.Fatalf("projection=%#v", got)
	}
}

func TestParseProjectionRejectsInvalidValues(t *testing.T) {
	for _, values := range []url.Values{
		{"attributes": {"userName", "displayName"}},
		{"attributes": {"active"}},
		{"excludedAttributes": {"unknown"}},
	} {
		if _, err := ParseProjection(values, ResourceGroups); err == nil {
			t.Fatalf("values=%v", values)
		}
	}
}
