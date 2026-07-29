package scim

import (
	"encoding/json"
	"strconv"
	"strings"
)

type Optional[T any] struct {
	Set   bool
	Value T
}

type UserMutation struct {
	Active                Optional[bool]
	UserName, DisplayName Optional[string]
	Name                  Optional[Name]
	Emails                Optional[[]Email]
}

type GroupMutation struct {
	ReplaceMembers            *[]int64
	AddMembers, RemoveMembers []int64
}

func ParseUserPatch(request PatchRequest) (UserMutation, error) {
	if len(request.Operations) > 100 {
		return UserMutation{}, parseError("invalidValue")
	}
	var mutation UserMutation
	for _, operation := range request.Operations {
		if err := applyUserOperation(&mutation, operation); err != nil {
			return UserMutation{}, err
		}
	}
	return mutation, nil
}

func applyUserOperation(mutation *UserMutation, operation PatchOperation) error {
	op := strings.ToLower(operation.Op)
	if op != "add" && op != "replace" && op != "remove" {
		return parseError("invalidValue")
	}
	if operation.Path == "" {
		if op == "remove" {
			return parseError("invalidPath")
		}
		var value map[string]json.RawMessage
		if json.Unmarshal(operation.Value, &value) != nil {
			return parseError("invalidValue")
		}
		for path, field := range value {
			if err := applyUserField(mutation, op, path, field); err != nil {
				return err
			}
		}
		return nil
	}
	return applyUserField(mutation, op, operation.Path, operation.Value)
}

func applyUserField(mutation *UserMutation, op, path string, value json.RawMessage) error {
	switch strings.ToLower(path) {
	case "id", "meta", "schemas":
		return parseError("mutability")
	case "active":
		if op == "remove" {
			mutation.Active = Optional[bool]{Set: true}
			return nil
		}
		return decode(value, &mutation.Active)
	case "username":
		return stringField(op, value, &mutation.UserName)
	case "displayname":
		return stringField(op, value, &mutation.DisplayName)
	case "name":
		if op == "remove" {
			mutation.Name = Optional[Name]{Set: true}
			return nil
		}
		return decode(value, &mutation.Name)
	case "emails":
		if op == "remove" {
			mutation.Emails = Optional[[]Email]{Set: true}
			return nil
		}
		return decode(value, &mutation.Emails)
	default:
		return parseError("invalidPath")
	}
}

func stringField(op string, value json.RawMessage, target *Optional[string]) error {
	if op == "remove" {
		*target = Optional[string]{Set: true}
		return nil
	}
	return decode(value, target)
}

func decode[T any](value json.RawMessage, target *Optional[T]) error {
	var decoded T
	if json.Unmarshal(value, &decoded) != nil {
		return parseError("invalidValue")
	}
	*target = Optional[T]{Set: true, Value: decoded}
	return nil
}

func ParseGroupPatch(request PatchRequest) (GroupMutation, error) {
	if len(request.Operations) > 100 {
		return GroupMutation{}, parseError("invalidValue")
	}
	var mutation GroupMutation
	for _, operation := range request.Operations {
		if err := applyGroupOperation(&mutation, operation); err != nil {
			return GroupMutation{}, err
		}
	}
	return mutation, nil
}

func applyGroupOperation(mutation *GroupMutation, operation PatchOperation) error {
	op := strings.ToLower(operation.Op)
	if op != "add" && op != "replace" && op != "remove" {
		return parseError("invalidValue")
	}
	if operation.Path == "" {
		if op == "remove" {
			return parseError("invalidPath")
		}
		var value map[string]json.RawMessage
		if json.Unmarshal(operation.Value, &value) != nil {
			return parseError("invalidValue")
		}
		members, ok := value["members"]
		if !ok || len(value) != 1 {
			return parseError("invalidPath")
		}
		operation.Path, operation.Value = "members", members
	}
	path := strings.ToLower(operation.Path)
	if path == "id" || path == "meta" || path == "schemas" {
		return parseError("mutability")
	}
	if op == "remove" && operation.Path == "" {
		return parseError("invalidPath")
	}
	if id, ok, err := memberFilter(operation.Path); err != nil {
		return err
	} else if ok {
		if op != "remove" {
			return parseError("invalidPath")
		}
		mutation.RemoveMembers = applyMemberDelta(mutation.RemoveMembers, &mutation.AddMembers, []int64{id})
		return nil
	}
	if path != "members" {
		return parseError("invalidPath")
	}
	if op == "remove" {
		return parseError("invalidPath")
	}
	members, err := memberIDs(operation.Value)
	if err != nil {
		return err
	}
	if op == "replace" {
		members = uniqueMemberIDs(members)
		mutation.ReplaceMembers = &members
		mutation.AddMembers = nil
		mutation.RemoveMembers = nil
	} else {
		mutation.AddMembers = applyMemberDelta(mutation.AddMembers, &mutation.RemoveMembers, members)
	}
	return nil
}

func applyMemberDelta(current []int64, opposite *[]int64, ids []int64) []int64 {
	for _, id := range ids {
		*opposite = withoutMemberID(*opposite, id)
		if !containsMemberID(current, id) {
			current = append(current, id)
		}
	}
	return current
}

func uniqueMemberIDs(ids []int64) []int64 {
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !containsMemberID(unique, id) {
			unique = append(unique, id)
		}
	}
	return unique
}

func containsMemberID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func withoutMemberID(ids []int64, remove int64) []int64 {
	for i, id := range ids {
		if id == remove {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

func memberFilter(path string) (int64, bool, error) {
	prefix, suffix := `members[value eq "`, `"]`
	if len(path) <= len(prefix)+len(suffix) || !strings.EqualFold(path[:len(prefix)], prefix) || !strings.HasSuffix(path, suffix) {
		return 0, false, nil
	}
	id, err := parseCanonicalID(path[len(prefix) : len(path)-len(suffix)])
	return id, true, err
}

func memberIDs(value json.RawMessage) ([]int64, error) {
	var members []struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(value, &members) != nil {
		return nil, parseError("invalidValue")
	}
	ids := make([]int64, len(members))
	for i, member := range members {
		id, err := parseCanonicalID(member.Value)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

func parseCanonicalID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != value {
		return 0, parseError("invalidValue")
	}
	return id, nil
}
