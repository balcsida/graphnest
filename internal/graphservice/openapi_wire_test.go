package graphservice

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

func TestOpenAPIExplorationWireResponses(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	definitions := document["components"].(map[string]any)["schemas"]
	validate := func(label, name string, value any) {
		t.Helper()
		rawSchema, err := json.Marshal(map[string]any{"$ref": "#/$defs/" + name, "$defs": definitions})
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal([]byte(strings.ReplaceAll(string(rawSchema), "#/components/schemas/", "#/$defs/")), &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var wire any
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(wire); err != nil {
			t.Errorf("%s: %v; wire=%s", label, err, encoded)
		}
	}

	service, backend, _ := exploreFixture()
	backend.generation.Producer = &graphv2.Producer{Name: "fixture", Version: "1"}
	backend.generation.ContentHash = []byte("hash")
	backend.generation.Capabilities = []string{}
	backend.matches[0].Fields = []string{}
	discovered, err := service.Discover(t.Context(), principalFor(101), api.GraphDiscoverRequest{Query: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	validate("populated discovery entity", "GraphEntityV2", discovered.Matches[0].Entity)

	explored, err := service.ExplorePublic(t.Context(), principalFor(101), api.GraphExploreRequest{Query: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	validate("populated exploration", "GraphExploreResponse", explored)

	service.Files = nil
	incomplete, err := service.ExplorePublic(t.Context(), principalFor(101), api.GraphExploreRequest{Query: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	file := incomplete.Files[0]
	file.Entities = []graphprotocol.Entity{}
	validate("incomplete source file", "GraphExploreFile", file)

	backend.matches = []graphprotocol.DiscoveryMatch{}
	backend.noEntry = true
	empty, err := service.ExplorePublic(t.Context(), principalFor(101), api.GraphExploreRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validate("empty exploration", "GraphExploreResponse", empty)

	entity := discovered.Matches[0].Entity
	entity.Fact.Location = nil
	validate("missing source location", "GraphExploreSelection", exploreSelection(entity, []InspectionSource{{Status: "ok", Content: "x", StartLine: 1, EndLine: 1}}))

	traversal := graphprotocol.TraverseResponse{Status: "ok", Entities: []graphprotocol.Entity{}, Generations: []graphprotocol.Generation{backend.generation}}
	validate("edgeless traversal", "GraphTraverseResponse", traversal)
	validate("empty files", "GraphExploreResponse/properties/files", empty.Files)
	validate("empty relationships", "GraphExploreResponse/properties/relationships", empty.Relationships)
	validate("incomplete source segments", "GraphExploreFile/properties/segments", file.Segments)
	validate("incomplete source selections", "GraphExploreFile/properties/selections", file.Selections)
	validate("edgeless traversal edges", "GraphTraverseResponse/properties/edges", traversal.Edges)

	backend.filesByPath = map[string]*graphv2.File{}
	missing, err := service.ExplorePublic(t.Context(), principalFor(101), api.GraphExploreRequest{Files: []string{"missing.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	validate("unindexed file entities", "GraphExploreFile/properties/entities", missing.Files[0].Entities)
}
