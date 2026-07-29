package scim

import (
	"encoding/json"
	"errors"
)

const (
	UserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	GroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
	ListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	PatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	ErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
)

var ErrPasswordNotSupported = errors.New("scim password is not supported")

type User struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id,omitempty"`
	ExternalID  string   `json:"externalId,omitempty"`
	UserName    string   `json:"userName,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Active      *bool    `json:"active,omitempty"`
	Name        Name     `json:"name,omitempty"`
	Emails      []Email  `json:"emails,omitempty"`
	Meta        Meta     `json:"meta"`
}

type Name struct {
	Formatted       string `json:"formatted,omitempty"`
	FamilyName      string `json:"familyName,omitempty"`
	GivenName       string `json:"givenName,omitempty"`
	MiddleName      string `json:"middleName,omitempty"`
	HonorificPrefix string `json:"honorificPrefix,omitempty"`
	HonorificSuffix string `json:"honorificSuffix,omitempty"`
}

type Email struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

type Group struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id,omitempty"`
	ExternalID  string   `json:"externalId,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Members     []Member `json:"members,omitempty"`
	Meta        Meta     `json:"meta"`
}

type Member struct {
	Value   string `json:"value"`
	Ref     string `json:"$ref,omitempty"`
	Display string `json:"display,omitempty"`
}

type Meta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	Location     string `json:"location,omitempty"`
}

type ListResponse[T any] struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    []T      `json:"Resources"`
}

type PatchRequest struct {
	Schemas    []string         `json:"schemas"`
	Operations []PatchOperation `json:"Operations"`
}

type PatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

func NewUser() User {
	return User{Schemas: []string{UserSchema}, Meta: Meta{ResourceType: "User"}}
}

func NewGroup() Group {
	return Group{Schemas: []string{GroupSchema}, Meta: Meta{ResourceType: "Group"}}
}

func NewListResponse[T any](resources []T, totalResults, startIndex int) ListResponse[T] {
	if resources == nil {
		resources = []T{}
	}
	return ListResponse[T]{
		Schemas:      []string{ListSchema},
		TotalResults: totalResults,
		StartIndex:   startIndex,
		ItemsPerPage: len(resources),
		Resources:    resources,
	}
}

func NewPatchRequest(operations []PatchOperation) PatchRequest {
	if operations == nil {
		operations = []PatchOperation{}
	}
	return PatchRequest{Schemas: []string{PatchSchema}, Operations: operations}
}

func (u *User) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, ok := fields["password"]; ok {
		return ErrPasswordNotSupported
	}
	type user User
	return json.Unmarshal(data, (*user)(u))
}
