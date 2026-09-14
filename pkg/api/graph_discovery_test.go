package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGraphDiscoveryRequestsExposeOnlyPublicSelectors(t *testing.T) {
	requests := []any{
		GraphDiscoverRequest{Repo: GraphRepositorySelector{ID: 101}, Branch: "main", Query: "hello"},
		GraphExploreRequest{Repo: GraphRepositorySelector{Name: "acme/one"}, Branch: "main", Query: "hello"},
		GraphFilesRequest{Repo: GraphRepositorySelector{ID: 101}, Branch: "main", Directory: "src"},
		GraphCapabilitiesRequest{Repo: GraphRepositorySelector{ID: 101}, Branch: "main"},
	}
	for _, request := range requests {
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"scope", "session_id", "repository_id"} {
			if strings.Contains(string(data), `"`+forbidden+`"`) {
				t.Fatalf("%T exposed %s: %s", request, forbidden, data)
			}
		}
	}
}
