package scim

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseUserPatchPathlessObject(t *testing.T) {
	got, err := ParseUserPatch(NewPatchRequest([]PatchOperation{{Op: "replace", Value: json.RawMessage(`{"active":false,"displayName":"Ada"}`)}}))
	if err != nil || !got.Active.Set || got.Active.Value || !got.DisplayName.Set || got.DisplayName.Value != "Ada" {
		t.Fatalf("mutation=%#v err=%v", got, err)
	}
}

func TestParseUserPatchRejectsReadonlyPath(t *testing.T) {
	_, err := ParseUserPatch(NewPatchRequest([]PatchOperation{{Op: "replace", Path: "id", Value: json.RawMessage(`"7"`)}}))
	if err == nil || !strings.Contains(err.Error(), "mutability") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseGroupPatchMemberships(t *testing.T) {
	got, err := ParseGroupPatch(NewPatchRequest([]PatchOperation{
		{Op: "add", Path: "members", Value: json.RawMessage(`[{"value":"7"}]`)},
		{Op: "remove", Path: `members[value eq "8"]`},
		{Op: "replace", Path: "members", Value: json.RawMessage(`[{"value":"9"}]`)},
	}))
	if err != nil || len(got.AddMembers) != 1 || got.AddMembers[0] != 7 || len(got.RemoveMembers) != 1 || got.RemoveMembers[0] != 8 || got.ReplaceMembers == nil || len(*got.ReplaceMembers) != 1 || (*got.ReplaceMembers)[0] != 9 {
		t.Fatalf("mutation=%#v err=%v", got, err)
	}
}

func TestParseGroupPatchRejectsMissingRemovePath(t *testing.T) {
	_, err := ParseGroupPatch(NewPatchRequest([]PatchOperation{{Op: "remove"}}))
	if err == nil || !strings.Contains(err.Error(), "invalidPath") {
		t.Fatalf("err=%v", err)
	}
}

func TestParsePatchRejectsTooManyOperations(t *testing.T) {
	operations := make([]PatchOperation, 101)
	if _, err := ParseUserPatch(NewPatchRequest(operations)); err == nil {
		t.Fatal("expected error")
	}
}
