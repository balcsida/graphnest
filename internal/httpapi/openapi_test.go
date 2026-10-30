package httpapi

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIAdminJobsPaginationContract(t *testing.T) {
	data, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var document struct {
		Paths map[string]struct {
			Get struct {
				Responses map[string]struct {
					Ref string `yaml:"$ref"`
				} `yaml:"responses"`
				Parameters []struct {
					Name     string `yaml:"name"`
					In       string `yaml:"in"`
					Required *bool  `yaml:"required"`
					Schema   struct {
						Type      string `yaml:"type"`
						MinLength int    `yaml:"minLength"`
					} `yaml:"schema"`
				} `yaml:"parameters"`
			} `yaml:"get"`
		} `yaml:"paths"`
		Components struct {
			Schemas map[string]struct {
				Required   []string `yaml:"required"`
				Properties map[string]struct {
					Type      string `yaml:"type"`
					MinLength int    `yaml:"minLength"`
					MaxItems  int    `yaml:"maxItems"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	adminJobs := document.Paths["/v1/admin/jobs"].Get
	parameters := adminJobs.Parameters
	if len(parameters) != 1 || parameters[0].Name != "cursor" || parameters[0].In != "query" || parameters[0].Required == nil || *parameters[0].Required || parameters[0].Schema.Type != "string" || parameters[0].Schema.MinLength != 1 {
		t.Fatalf("admin jobs cursor parameter = %#v, want optional non-empty query string", parameters)
	}
	if response := adminJobs.Responses["400"]; response.Ref != "#/components/responses/InvalidRequest" {
		t.Errorf("admin jobs 400 response = %#v, want InvalidRequest", response)
	}

	jobs := document.Components.Schemas["AdminJobList"]
	if jobs.Properties["jobs"].MaxItems != 25 {
		t.Errorf("AdminJobList.jobs.maxItems = %d, want 25", jobs.Properties["jobs"].MaxItems)
	}
	nextCursor := jobs.Properties["next_cursor"]
	if nextCursor.Type != "string" || nextCursor.MinLength != 1 || slices.Contains(jobs.Required, "next_cursor") {
		t.Errorf("AdminJobList.next_cursor = %#v with required %v, want optional non-empty string", nextCursor, jobs.Required)
	}
}

func TestOpenAPIAdvisoryContract(t *testing.T) {
	data, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	dig := func(keys ...string) any {
		var value any = document
		for _, key := range keys {
			object, ok := value.(map[string]any)
			if !ok {
				return nil
			}
			value = object[key]
		}
		return value
	}
	for _, route := range [][2]string{
		{"/v1/supply-chain/advisories", "get"},
		{"/v1/supply-chain/advisories", "post"},
		{"/v1/supply-chain/advisory-checks", "post"},
		{"/v1/supply-chain/advisories/{id}", "get"},
		{"/v1/supply-chain/advisories/{id}", "put"},
		{"/v1/supply-chain/advisories/{id}/versions/{version}", "get"},
		{"/v1/supply-chain/advisories/{id}/close", "post"},
		{"/v1/supply-chain/advisories/{id}/reopen", "post"},
		{"/v1/supply-chain/advisories/{id}/repositories", "get"},
		{"/v1/supply-chain/advisories/{id}/repositories/{repository_id}", "get"},
		{"/v1/supply-chain/advisories/{id}/repositories/{repository_id}/exceptions", "post"},
		{"/v1/supply-chain/advisories/{id}/export.csv", "get"},
		{"/v1/supply-chain/advisories/{id}/export.md", "get"},
	} {
		if dig("paths", route[0], route[1]) == nil {
			t.Errorf("missing %s %s", route[1], route[0])
		}
	}
	if got := dig("components", "schemas", "AdvisoryRepositoryList", "properties", "repositories", "maxItems"); got != 100 {
		t.Errorf("AdvisoryRepositoryList.repositories.maxItems = %v, want 100", got)
	}
	if got := dig("components", "schemas", "AdvisoryRepositoryList", "properties", "next_cursor", "minLength"); got != 1 {
		t.Errorf("AdvisoryRepositoryList.next_cursor.minLength = %v, want 1", got)
	}
	if required, _ := dig("components", "schemas", "AdvisoryRepositoryList", "required").([]any); slices.Contains(required, any("next_cursor")) {
		t.Error("AdvisoryRepositoryList.next_cursor must be optional")
	}
	if got := dig("components", "schemas", "AdvisoryExceptionRequest", "required"); !reflect.DeepEqual(got, []any{"kind", "reason", "basis"}) {
		t.Errorf("AdvisoryExceptionRequest.required = %v, want [kind reason basis]", got)
	}
	if got := dig("components", "schemas", "AdvisoryDefinition", "additionalProperties"); got != false {
		t.Errorf("AdvisoryDefinition.additionalProperties = %v, want false", got)
	}
	for _, status := range []string{"413", "415"} {
		if dig("paths", "/v1/supply-chain/advisories", "post", "responses", status) == nil {
			t.Errorf("advisory creation does not document %s", status)
		}
	}
	for _, media := range []string{"application/yaml", "application/json"} {
		if dig("paths", "/v1/supply-chain/advisories", "post", "requestBody", "content", media) == nil {
			t.Errorf("advisory creation does not accept %s", media)
		}
	}
}
