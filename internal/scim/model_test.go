package scim

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestUserJSON(t *testing.T) {
	active := true
	user := NewUser()
	user.ID = "42"
	user.ExternalID = "directory-42"
	user.UserName = "ada@example.test"
	user.DisplayName = "Ada Lovelace"
	user.Active = &active
	user.Name = Name{Formatted: "Ada Lovelace", GivenName: "Ada", FamilyName: "Lovelace"}
	user.Emails = []Email{{Value: "ada@example.test", Type: "work", Primary: true}}
	user.Meta = Meta{ResourceType: "User", Created: "2026-07-29T12:00:00Z", LastModified: "2026-07-29T12:01:00Z", Location: "https://example.test/scim/v2/Users/42"}

	got, err := json.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"schemas":["`+UserSchema+`"]`)) ||
		!bytes.Contains(got, []byte(`"externalId":"directory-42"`)) ||
		!bytes.Contains(got, []byte(`"resourceType":"User"`)) {
		t.Fatalf("json=%s", got)
	}

	var roundTrip User
	if err := json.Unmarshal(got, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.ID != user.ID || roundTrip.UserName != user.UserName || roundTrip.Meta.Location != user.Meta.Location || len(roundTrip.Emails) != 1 || !roundTrip.Emails[0].Primary {
		t.Fatalf("round trip=%#v", roundTrip)
	}
}

func TestGroupJSON(t *testing.T) {
	group := NewGroup()
	group.ID = "7"
	group.ExternalID = "engineering"
	group.DisplayName = "Engineering"
	group.Members = []Member{{Value: "42", Ref: "https://example.test/scim/v2/Users/42", Display: "Ada Lovelace"}}
	group.Meta = Meta{ResourceType: "Group", Location: "https://example.test/scim/v2/Groups/7"}

	got, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"schemas":["`+GroupSchema+`"]`)) ||
		!bytes.Contains(got, []byte(`"$ref":"https://example.test/scim/v2/Users/42"`)) ||
		!bytes.Contains(got, []byte(`"resourceType":"Group"`)) {
		t.Fatalf("json=%s", got)
	}

	var roundTrip Group
	if err := json.Unmarshal(got, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.ID != group.ID || roundTrip.Meta.Location != group.Meta.Location || len(roundTrip.Members) != 1 || roundTrip.Members[0].Ref != group.Members[0].Ref {
		t.Fatalf("round trip=%#v", roundTrip)
	}
}

func TestListResponseJSON(t *testing.T) {
	response := NewListResponse([]User{}, 0, 1)
	got, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{
		[]byte(`"schemas":["` + ListSchema + `"]`),
		[]byte(`"totalResults":0`),
		[]byte(`"startIndex":1`),
		[]byte(`"itemsPerPage":0`),
		[]byte(`"Resources":[]`),
	} {
		if !bytes.Contains(got, want) {
			t.Fatalf("json=%s missing=%s", got, want)
		}
	}
}

func TestPatchRequestJSON(t *testing.T) {
	patch := NewPatchRequest([]PatchOperation{{Op: "replace", Path: "active", Value: json.RawMessage(`false`)}})
	got, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"schemas":["`+PatchSchema+`"]`)) ||
		!bytes.Contains(got, []byte(`"Operations":[{"op":"replace","path":"active","value":false}]`)) {
		t.Fatalf("json=%s", got)
	}
}

func TestPatchRequestJSONHasOperationsArray(t *testing.T) {
	got, err := json.Marshal(NewPatchRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"Operations":[]`)) {
		t.Fatalf("json=%s", got)
	}
}

func TestUserJSONRejectsPassword(t *testing.T) {
	var user User
	if err := json.Unmarshal([]byte(`{"userName":"ada","password":"secret"}`), &user); err == nil {
		t.Fatal("password was accepted")
	}
}
