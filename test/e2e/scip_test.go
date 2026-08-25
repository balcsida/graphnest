//go:build e2e && unix

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestSCIPCrossRepository(t *testing.T) {
	database := newMilestoneDatabase(t)
	const (
		definitionID  = int64(101)
		referenceID   = int64(102)
		definitionSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		referenceSHA  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		symbol        = "scip-go gomod example.com/acme/lib v1.0.0 Item#"
	)
	if err := database.store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{GitHubID: milestoneInstallationID, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		id   int64
		name string
		sha  string
	}{{definitionID, "definition", definitionSHA}, {referenceID, "reference", referenceSHA}} {
		repository, err := database.store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{GitHubID: fixture.id, InstallationID: milestoneInstallationID, Owner: "acme", Name: fixture.name, CloneURL: "https://example.invalid/acme/" + fixture.name + ".git", WebURL: "https://example.invalid/acme/" + fixture.name, DefaultBranch: "main", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.pool.Exec(t.Context(), "update repositories set indexed_sha=$2, desired_sha=$2, status='ready' where id=$1", repository.ID, fixture.sha); err != nil {
			t.Fatal(err)
		}
	}

	authenticator := authn.NewStatic(map[string]authn.Principal{
		"admin":          {Subject: "admin", InstallationID: milestoneInstallationID, RepositoryIDs: []int64{definitionID, referenceID}, Administrator: true},
		"both":           {Subject: "both", InstallationID: milestoneInstallationID, RepositoryIDs: []int64{definitionID, referenceID}},
		"reference-only": {Subject: "reference-only", InstallationID: milestoneInstallationID, RepositoryIDs: []int64{referenceID}},
	})
	mux := http.NewServeMux()
	httpapi.RegisterSCIP(mux, authn.RequestAuthenticator{Bearer: authenticator}, &scipgraph.Service{Store: database.store}, 64<<10, 64<<20, 256<<10)
	server := httptest.NewServer(mux)
	defer server.Close()

	uploadSCIP(t, server.URL, "admin", definitionID, definitionSHA, scipIndex(t, "definition.go", symbol, int32(scip.SymbolRole_Definition)))
	uploadSCIP(t, server.URL, "admin", referenceID, referenceSHA, scipIndex(t, "reference.go", symbol, int32(scip.SymbolRole_ReadAccess)))

	request := api.SCIPNavigationRequest{RepositoryID: referenceID, Path: "reference.go", Line: 1, Character: 0, Operation: "definitions"}
	locations := navigateSCIP(t, server.URL, "both", request)
	if len(locations) != 1 || locations[0].RepositoryID != definitionID || locations[0].Commit != definitionSHA || locations[0].Path != "definition.go" {
		t.Fatalf("authorized locations = %#v", locations)
	}
	if locations := navigateSCIP(t, server.URL, "reference-only", request); len(locations) != 0 {
		t.Fatalf("unauthorized definition leaked: %#v", locations)
	}
}

func scipIndex(t *testing.T, path, symbol string, roles int32) []byte {
	t.Helper()
	data, err := proto.Marshal(&scip.Index{
		Metadata:  &scip.Metadata{ProjectRoot: "file:///workspace", ToolInfo: &scip.ToolInfo{Name: "scip-go", Version: "test"}},
		Documents: []*scip.Document{{RelativePath: path, PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, Occurrences: []*scip.Occurrence{{Range: []int32{0, 0, 4}, Symbol: symbol, SymbolRoles: roles}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func uploadSCIP(t *testing.T, baseURL, token string, repositoryID int64, commit string, data []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("%s/v1/scip/uploads?repository_id=%d&commit=%s", baseURL, repositoryID, commit), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/vnd.scip+protobuf")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("upload repository %d status = %d, want %d", repositoryID, response.StatusCode, http.StatusNoContent)
	}
}

func navigateSCIP(t *testing.T, baseURL, token string, input api.SCIPNavigationRequest) []api.SCIPLocation {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/scip/navigation", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("navigation status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var output api.SCIPNavigationResponse
	if err := json.NewDecoder(response.Body).Decode(&output); err != nil {
		t.Fatal(err)
	}
	return output.Locations
}
