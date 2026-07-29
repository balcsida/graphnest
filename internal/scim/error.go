package scim

import "encoding/json"

type Error struct {
	Status   int    `json:"status,string"`
	SCIMType string `json:"scimType,omitempty"`
	Detail   string `json:"detail,omitempty"`
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
