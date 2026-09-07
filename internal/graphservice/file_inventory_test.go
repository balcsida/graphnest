package graphservice

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

type inventoryBackend struct {
	*inspectionBackend
	page    graphprotocol.FilesResponse
	request graphprotocol.FilesRequest
	after   func()
}

func (b *inventoryBackend) IndexedFiles(_ context.Context, r graphprotocol.FilesRequest) (graphprotocol.FilesResponse, error) {
	b.request = r
	if b.after != nil {
		b.after()
	}
	return b.page, nil
}

func inventoryFixture() (*Service, *inventoryBackend, *fakeRepositoryStore) {
	s, base, store := inspectionFixture()
	b := &inventoryBackend{inspectionBackend: base, page: graphprotocol.FilesResponse{Generations: []graphprotocol.Generation{base.generation}, TotalFiles: proto.Int64(3)}}
	for _, path := range []string{"Root.ts", "src/a.ts", "src/deep/b.ts"} {
		b.page.Files = append(b.page.Files, graphprotocol.IndexedFile{RepositoryID: 101, Fact: &graphv2.File{Path: path, Language: "typescript", Generated: proto.Bool(true), NodeCount: proto.Int64(0)}})
	}
	s.Backend = b
	return s, b, store
}

func TestFileInventoryViews(t *testing.T) {
	s, b, _ := inventoryFixture()
	r := FileInventoryRequest{Repo: api.GraphRepositorySelector{ID: 101}, MaxDepth: new(1)}
	got, err := s.FileInventory(t.Context(), principalFor(101), r)
	if err != nil || got.Complete || got.TotalFiles != 3 || got.PageFiles != 3 || got.VisibleFiles != 1 || !slices.Contains(got.Boundaries, "max_depth") || len(got.Tree) != 2 || got.Tree[0].Path != "src" || !got.Tree[0].Truncated {
		t.Fatalf("bounded tree=%+v err=%v", got, err)
	}
	if !proto.Equal(got.Tree[1].File.Metadata, b.page.Files[0].Fact) {
		t.Fatal("original file metadata changed")
	}
	r.Format = "grouped"
	got, err = s.FileInventory(t.Context(), principalFor(101), r)
	if err != nil || !got.Complete || len(got.Groups) != 1 || len(got.Groups[0].Files) != 3 {
		t.Fatalf("groups=%+v err=%v", got, err)
	}
	r.Format = "flat"
	r.IncludeMetadata = proto.Bool(false)
	got, err = s.FileInventory(t.Context(), principalFor(101), r)
	if err != nil || len(got.Files) != 3 || got.Files[0].Metadata != nil || got.Files[0].Path != "Root.ts" {
		t.Fatalf("metadata config=%+v err=%v", got, err)
	}
	for _, cursor := range []string{"", "continuation"} {
		r.Cursor = cursor
		b.page.Files = b.page.Files[:1]
		b.page.NextCursor = ""
		if cursor == "" {
			b.page.NextCursor = "next"
		}
		got, err = s.FileInventory(t.Context(), principalFor(101), r)
		if err != nil || got.Complete || !slices.Contains(got.Boundaries, "file_page") || got.TotalFiles != 3 || got.PageFiles != 1 {
			t.Fatalf("page=%+v err=%v", got, err)
		}
	}
}

func TestFileInventoryAuthorityAndBounds(t *testing.T) {
	for _, mode := range []string{"grant", "sha", "generation", "hidden", "canceled", "output", "missing_count"} {
		t.Run(mode, func(t *testing.T) {
			s, b, store := inventoryFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b.after = func() {
				switch mode {
				case "grant":
					store.repositories = nil
				case "sha":
					store.repositories[0].IndexedSHA = strings.Repeat("b", 40)
				case "generation":
					b.changed = true
				case "canceled":
					cancel()
				}
			}
			if mode == "hidden" {
				b.page.Files[2].RepositoryID = 999
			}
			if mode == "output" {
				s.Limits.MaxResponseBytes = 32
			}
			if mode == "missing_count" {
				b.page.TotalFiles = nil
			}
			got, err := s.FileInventory(ctx, principalFor(101), FileInventoryRequest{Repo: api.GraphRepositorySelector{ID: 101}})
			if err == nil || !reflect.DeepEqual(got, FileInventoryResponse{}) {
				t.Fatalf("leaked %s: %+v err=%v", mode, got, err)
			}
		})
	}
	for _, r := range []FileInventoryRequest{{Path: "../secret"}, {Path: "/../secret"}, {Path: "a\x00b"}, {Path: strings.Repeat("x", 16385)}, {Pattern: strings.Repeat("*", 1025)}, {Pattern: "a\x00b"}, {Limit: 101}, {Limit: -1}, {Cursor: strings.Repeat("x", 513)}, {Format: "unknown"}} {
		s, _, _ := inventoryFixture()
		r.Repo = api.GraphRepositorySelector{ID: 101}
		if _, err := s.FileInventory(t.Context(), principalFor(101), r); !errors.Is(err, ErrInvalidRequest) && !errors.Is(err, graphquery.ErrInvalidRequest) {
			t.Fatalf("invalid request=%+v err=%v", r, err)
		}
	}
}

func TestFileInventoryPrefixConfiguration(t *testing.T) {
	for _, path := range []string{"/", ".", "./", "", "\\", "//", ".//", "/./", "\\src\\", "./src/", "src//deep"} {
		s, b, _ := inventoryFixture()
		_, err := s.FileInventory(t.Context(), principalFor(101), FileInventoryRequest{Repo: api.GraphRepositorySelector{ID: 101}, Path: path, Pattern: "*.ts"})
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if strings.Contains(path, "src") {
			want = "src"
		}
		if path == "src//deep" {
			want = path
		}
		if b.request.Prefix == nil || *b.request.Prefix != want || b.request.Pattern != "*.ts" || !b.request.IncludeCount {
			t.Fatalf("prefix %q became %+v", path, b.request)
		}
	}
}

func TestFileInventoryDepthCeiling(t *testing.T) {
	for _, depth := range []int{-1, 0, 1, 2, 20, 21, 1000000} {
		s, b, _ := inventoryFixture()
		b.page.Files = nil
		b.page.TotalFiles = proto.Int64(100)
		for index := 0; index < 100; index++ {
			b.page.Files = append(b.page.Files, graphprotocol.IndexedFile{RepositoryID: 101, Fact: &graphv2.File{Path: fmt.Sprintf("dir%03d/", index) + strings.Repeat("x/", 8000) + "leaf.ts"}})
		}
		got, err := s.FileInventory(t.Context(), principalFor(101), FileInventoryRequest{Repo: api.GraphRepositorySelector{ID: 101}, MaxDepth: &depth, IncludeMetadata: proto.Bool(false)})
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		var walk func([]*InventoryTree)
		walk = func(nodes []*InventoryTree) {
			for _, node := range nodes {
				count++
				walk(node.Children)
			}
		}
		walk(got.Tree)
		if count != 100*max(1, min(20, depth)) || got.Complete || got.VisibleFiles != 0 || got.PageFiles != 100 {
			t.Fatalf("depth=%d nodes=%d result=%+v", depth, count, got)
		}
	}
}
