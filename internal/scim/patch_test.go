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
	if err != nil || len(got.AddMembers) != 0 || len(got.RemoveMembers) != 0 || got.ReplaceMembers == nil || len(*got.ReplaceMembers) != 1 || (*got.ReplaceMembers)[0] != 9 {
		t.Fatalf("mutation=%#v err=%v", got, err)
	}
}

func TestParseGroupPatchNormalizesMemberOperationOrder(t *testing.T) {
	for _, test := range []struct {
		name        string
		operations  []PatchOperation
		replace     []int64
		hasReplace  bool
		add, remove []int64
	}{
		{
			name: "replace clears prior add and remove",
			operations: []PatchOperation{
				{Op: "add", Path: "members", Value: json.RawMessage(`[{"value":"7"}]`)},
				{Op: "remove", Path: `members[value eq "8"]`},
				{Op: "replace", Path: "members", Value: json.RawMessage(`[{"value":"9"}]`)},
			},
			replace: []int64{9}, hasReplace: true,
		},
		{
			name: "add after replace stays a delta",
			operations: []PatchOperation{
				{Op: "replace", Path: "members", Value: json.RawMessage(`[{"value":"9"}]`)},
				{Op: "add", Path: "members", Value: json.RawMessage(`[{"value":"7"}]`)},
			},
			replace: []int64{9}, hasReplace: true, add: []int64{7},
		},
		{
			name: "remove supersedes prior add",
			operations: []PatchOperation{
				{Op: "add", Path: "members", Value: json.RawMessage(`[{"value":"7"},{"value":"7"}]`)},
				{Op: "remove", Path: `members[value eq "7"]`},
			},
			remove: []int64{7},
		},
		{
			name: "add supersedes prior remove",
			operations: []PatchOperation{
				{Op: "remove", Path: `members[value eq "7"]`},
				{Op: "add", Path: "members", Value: json.RawMessage(`[{"value":"7"},{"value":"7"}]`)},
			},
			add: []int64{7},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseGroupPatch(NewPatchRequest(test.operations))
			if err != nil || (test.hasReplace != (got.ReplaceMembers != nil)) || (got.ReplaceMembers != nil && !sameIDs(*got.ReplaceMembers, test.replace)) || !sameIDs(got.AddMembers, test.add) || !sameIDs(got.RemoveMembers, test.remove) {
				t.Fatalf("mutation=%#v err=%v", got, err)
			}
		})
	}
}

func sameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
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

func TestParsePatchRequiresOperations(t *testing.T) {
	for _, parse := range []func(PatchRequest) error{
		func(request PatchRequest) error { _, err := ParseUserPatch(request); return err },
		func(request PatchRequest) error { _, err := ParseGroupPatch(request); return err },
	} {
		if err := parse(PatchRequest{Schemas: []string{PatchSchema}}); err == nil || !strings.Contains(err.Error(), "invalidValue") {
			t.Fatalf("omitted Operations err=%v", err)
		}
		if err := parse(NewPatchRequest(nil)); err == nil || !strings.Contains(err.Error(), "invalidValue") {
			t.Fatalf("empty Operations err=%v", err)
		}
	}
}

func TestParsePatchRejectsUnknownAndTrailingNestedJSON(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{"user unknown name field", func() error {
			_, err := ParseUserPatch(NewPatchRequest([]PatchOperation{{Op: "replace", Path: "name", Value: json.RawMessage(`{"givenName":"Ada","unknown":true}`)}}))
			return err
		}},
		{"user trailing name value", func() error {
			_, err := ParseUserPatch(NewPatchRequest([]PatchOperation{{Op: "replace", Path: "name", Value: json.RawMessage(`{"givenName":"Ada"} {}`)}}))
			return err
		}},
		{"group unknown member field", func() error {
			_, err := ParseGroupPatch(NewPatchRequest([]PatchOperation{{Op: "replace", Path: "members", Value: json.RawMessage(`[{"value":"1","unknown":true}]`)}}))
			return err
		}},
		{"group trailing member value", func() error {
			_, err := ParseGroupPatch(NewPatchRequest([]PatchOperation{{Op: "replace", Path: "members", Value: json.RawMessage(`[{"value":"1"}] {}`)}}))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil {
				t.Fatal("invalid nested JSON accepted")
			}
		})
	}
}
