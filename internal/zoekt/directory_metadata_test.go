package zoekt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/search"
)

func TestSearchAnnotatesDirectoryMatchesFromScopedMetadata(t *testing.T) {
	requested := []uint32{7, 8, 10, 11, 12}
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/search":
			_, _ = writer.Write([]byte(`{"Result":{"Files":[
				{"FileName":"directory.go","RepositoryID":7,"LineMatches":[{"Line":"ZGlyZWN0b3J5Cg==","LineNumber":1}]},
				{"FileName":"git.go","RepositoryID":8,"Version":"git-sha","Branches":["git"],"LineMatches":[{"Line":"Z2l0Cg==","LineNumber":1}]},
				{"FileName":"ambiguous.go","RepositoryID":10,"LineMatches":[{"Line":"YW1iaWd1b3VzCg==","LineNumber":1}]},
				{"FileName":"missing.go","RepositoryID":11,"LineMatches":[{"Line":"bWlzc2luZwo=","LineNumber":1}]},
				{"FileName":"incomplete.go","RepositoryID":12,"Branches":["main"],"LineMatches":[{"Line":"aW5jb21wbGV0ZQo=","LineNumber":1}]},
				{"FileName":"unauthorized.go","RepositoryID":99,"LineMatches":[{"Line":"dW5hdXRob3JpemVkCg==","LineNumber":1}]}
			]}}`))
		case "/api/list":
			listCalls++
			var body struct {
				Q string `json:"Q"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Q != "meta.grepnest_repository_id:7 or meta.grepnest_repository_id:8 or meta.grepnest_repository_id:10 or meta.grepnest_repository_id:11 or meta.grepnest_repository_id:12" {
				t.Fatalf("metadata scope = %q", body.Q)
			}
			_, _ = writer.Write([]byte(`{"List":{"ReposMap":{
				"7":{"Branches":[{"Name":"main","Version":"directory-sha"}]},
				"8":{"Branches":[{"Name":"other","Version":"other-sha"}]},
				"10":{"Branches":[{"Name":"main","Version":"one"},{"Name":"release","Version":"two"}]},
				"12":{"Branches":[{"Name":"main","Version":"twelve"}]},
				"99":{"Branches":[{"Name":"main","Version":"unauthorized-sha"}]}
			}}}`))
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), 64<<10, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(t.Context(), search.BackendRequest{Query: "needle", RepositoryIDs: requested, Limit: 100, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if listCalls != 1 {
		t.Fatalf("list calls = %d", listCalls)
	}
	if len(response.Matches) != 2 {
		t.Fatalf("matches = %#v", response.Matches)
	}
	if got := response.Matches[0]; got.ZoektID != 7 || got.SHA != "directory-sha" || !slices.Equal(got.Branches, []string{"main"}) {
		t.Fatalf("directory match = %#v", got)
	}
	if got := response.Matches[1]; got.ZoektID != 8 || got.SHA != "git-sha" || !slices.Equal(got.Branches, []string{"git"}) {
		t.Fatalf("git match changed = %#v", got)
	}
}
