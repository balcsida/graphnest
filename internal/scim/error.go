package scim

import (
	"encoding/json"
	"errors"
)

type Error struct {
	Status   int    `json:"status,string"`
	SCIMType string `json:"scimType,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

func ParseRequestError(err error) (Error, bool) {
	var parsed parseError
	if !errors.As(err, &parsed) {
		return Error{}, false
	}
	return Error{Status: 400, SCIMType: parsed.Error(), Detail: "SCIM request is invalid"}, true
}

func (e Error) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Schemas  []string `json:"schemas"`
		Status   int      `json:"status,string"`
		SCIMType string   `json:"scimType,omitempty"`
		Detail   string   `json:"detail,omitempty"`
	}{
		Schemas:  []string{ErrorSchema},
		Status:   e.Status,
		SCIMType: e.SCIMType,
		Detail:   e.Detail,
	})
}
