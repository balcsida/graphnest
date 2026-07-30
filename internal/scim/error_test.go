package scim

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestErrorJSON(t *testing.T) {
	got, err := json.Marshal(Error{Status: 400, SCIMType: "invalidFilter", Detail: "filter is unsupported"})
	if err != nil || !bytes.Contains(got, []byte(`"urn:ietf:params:scim:api:messages:2.0:Error"`)) {
		t.Fatalf("json=%s err=%v", got, err)
	}
	if !bytes.Contains(got, []byte(`"status":"400"`)) || !bytes.Contains(got, []byte(`"scimType":"invalidFilter"`)) {
		t.Fatalf("json=%s", got)
	}
}

func TestErrorJSONOmitsEmptyOptionalFields(t *testing.T) {
	got, err := json.Marshal(Error{Status: 404})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("scimType")) || bytes.Contains(got, []byte("detail")) {
		t.Fatalf("json=%s", got)
	}
}
