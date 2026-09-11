//go:build integration

package postgres

import (
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestGraphIndexedFiles(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Files = nil
	for _, p := range []string{"src/A.ts", "src/b.ts", "src/deep/c.ts", "src/file-only.txt", "srcx/no.ts", "other/no.ts"} {
		a.Files = append(a.Files, &graphv2.File{Path: p, ContentHash: testSHA('a') + testSHA('a')[:24], Size: 42, Generated: proto.Bool(true)})
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "files"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	request := graphprotocol.FilesRequest{Scope: scope, Directory: "./src/", Glob: "src/**/*.ts", Limit: 1}
	paths := []string{}
	for {
		page, err := service.IndexedFiles(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range page.Files {
			if file.RepositoryID != 101 || file.Fact.Size != 42 || !file.Fact.GetGenerated() {
				t.Fatalf("lost metadata=%+v", file)
			}
			paths = append(paths, file.Fact.Path)
		}
		if page.NextCursor == "" {
			break
		}
		request.Cursor = page.NextCursor
	}
	if len(paths) != 3 || paths[0] != "src/A.ts" || paths[1] != "src/b.ts" || paths[2] != "src/deep/c.ts" {
		t.Fatalf("paths=%v", paths)
	}
	got, err := service.IndexedFiles(t.Context(), graphprotocol.FilesRequest{Scope: scope, Path: proto.String("./src/file-only.txt")})
	if err != nil || len(got.Files) != 1 || got.Files[0].Fact.Path != "src/file-only.txt" {
		t.Fatalf("file-only=%+v err=%v", got, err)
	}
	got, err = service.IndexedFiles(t.Context(), graphprotocol.FilesRequest{Scope: scope, Glob: "src/a.ts"})
	if err != nil || len(got.Files) != 0 {
		t.Fatalf("case-sensitive=%+v err=%v", got, err)
	}
}
