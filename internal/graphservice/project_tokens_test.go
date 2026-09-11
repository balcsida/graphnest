package graphservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/pkg/api"
)

func TestProjectTokensPinnedOracle(t *testing.T) {
	data, err := os.ReadFile("../../test/fixtures/codegraph/discovery-variants.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Calls []struct {
			ID     string
			Answer json.RawMessage
		}
	}
	if err = json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	var want struct{ SortedMembership []string }
	for _, c := range oracle.Calls {
		if c.ID == "project-name-tokens" {
			if err = json.Unmarshal(c.Answer, &want); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(want.SortedMembership) != 2 {
		t.Fatal("missing frozen project token answer")
	}
	s, _, store := inspectionFixture()
	store.repositories[0].Name = "acme/project"
	other := readyRepository("acme/hiddenproject")
	other.ID, other.GitHubID = 2, 202
	store.repositories = append(store.repositories, other)
	manifest, err := os.ReadFile("../../test/fixtures/codegraph/source/package.json")
	if err != nil {
		t.Fatal(err)
	}
	s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		if r.RepositoryID != 101 {
			t.Fatal("read another repository's project manifests")
		}
		if r.Path == "go.mod" {
			return api.ReadFileResponse{}, errors.New("missing")
		}
		return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, StartLine: 1, EndLine: 1, Content: string(manifest)}, nil
	})
	got, err := s.ProjectNameTokens(t.Context(), principalFor(101), ProjectTokensRequest{Repo: api.GraphRepositorySelector{ID: 101}})
	if err != nil || !reflect.DeepEqual(got.Tokens, want.SortedMembership) {
		t.Fatalf("pinned=%+v want=%v err=%v", got, want.SortedMembership, err)
	}
	for _, term := range graphquery.DiscoveryTerms("project graphnestparityfixture greeting", got.Tokens) {
		if term == "project" || term == "graphnestparityfixture" {
			t.Fatal("project tokens did not feed discovery policy")
		}
	}
}

func TestProjectTokensExactSource(t *testing.T) {
	for _, tc := range []struct {
		name, repo, module, pkg string
		want                    []string
	}{{"three", "acme/Repo-Name", "module example.org/Module-Name\n", "{\"name\":\"@scope/Package-Name\"}", []string{"modulename", "packagename", "reponame"}}, {"duplicate", "acme/project", "module example/project", "{\"name\":\"project\"}", []string{"project"}}, {"short", "acme/api", "module example/core", "{\"name\":\"app\"}", nil}, {"malformed", "acme/visible", "not a module", "{", []string{"visible"}}, {"ascii", "acme/éRepo_Name", "module example/áAlphaName", "{\"name\":\"ÉPackage\"}", []string{"alphaname", "package", "reponame"}}} {
		t.Run(tc.name, func(t *testing.T) {
			s, b, store := inspectionFixture()
			store.repositories[0].Name = tc.repo
			reads := 0
			s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				reads++
				if r.RepositoryID != 101 || sha != b.generation.Commit || r.StartLine != 1 || r.EndLine <= 0 || r.EndLine > 512 {
					t.Fatalf("unbounded/unselected read: %+v %s", r, sha)
				}
				content := tc.module
				if r.Path == "package.json" {
					content = tc.pkg
				} else if r.Path != "go.mod" {
					t.Fatal(r.Path)
				}
				return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, StartLine: 1, EndLine: 1, Content: content}, nil
			})
			got, err := s.ProjectNameTokens(t.Context(), principalFor(101), ProjectTokensRequest{Repo: api.GraphRepositorySelector{ID: 101}})
			if err != nil || !reflect.DeepEqual(got.Tokens, tc.want) || reads != 2 {
				t.Fatalf("got=%+v err=%v reads=%d", got, err, reads)
			}
		})
	}
}

func TestProjectTokensModuleDeclarations(t *testing.T) {
	const valid = "module example.org/moduleproject\n"
	for _, tc := range []struct {
		name, manifest string
		valid          bool
	}{
		{"lf", valid + "\ngo 1.26.6\n", true},
		{"crlf", "module example.org/moduleproject\r\n\r\ngo 1.26.6\r\n", true},
		{"quoted_crlf", "\tmodule \"example.org/moduleproject\" // comment\r\n", true},
		{"commented_declaration", "// module example.org/ignored\n" + valid, true},
		{"duplicate_valid", valid + valid, false},
		{"malformed_second", valid + "module example.org/anotherproject extra\n", false},
		{"malformed_first", "module example.org/anotherproject extra\n" + valid, false},
		{"missing_path_second", valid + "module\n", false},
		{"missing_space_second", valid + "module\"example.org/anotherproject\"\n", false},
		{"malformed_only", "module example.org/moduleproject extra\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := inspectionFixture()
			s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				if r.Path != "go.mod" {
					return api.ReadFileResponse{}, errors.New("missing")
				}
				return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, StartLine: 1, EndLine: strings.Count(tc.manifest, "\n") + 1, Content: tc.manifest}, nil
			})
			want := []string{"visible"}
			if tc.valid {
				want = []string{"moduleproject", "visible"}
			}
			got, err := s.ProjectNameTokens(t.Context(), principalFor(101), ProjectTokensRequest{Repo: api.GraphRepositorySelector{ID: 101}})
			if err != nil || !reflect.DeepEqual(got.Tokens, want) {
				t.Fatalf("module evidence=%v want=%v err=%v", got.Tokens, want, err)
			}
		})
	}
}

func TestProjectTokensAuthorityAndUnavailable(t *testing.T) {
	for _, change := range []string{"grant", "sha", "generation", "response_sha", "response_repo", "response_path", "cancel", "missing", "unavailable", "truncated", "oversized", "line_ceiling", "nul_manifest", "invalid_scope"} {
		t.Run(change, func(t *testing.T) {
			s, b, store := inspectionFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				calls++
				v := api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, StartLine: 1, EndLine: 1, Content: "{\"name\":\"package\"}"}
				switch change {
				case "grant":
					store.repositories = nil
				case "sha":
					store.repositories[0].IndexedSHA = strings.Repeat("b", 40)
				case "generation":
					b.changed = true
				case "response_sha":
					v.IndexedSHA = "other"
				case "response_repo":
					v.RepositoryID = 202
				case "response_path":
					v.Path = "other"
				case "cancel":
					cancel()
				case "missing":
					return api.ReadFileResponse{}, errors.New("missing")
				case "truncated":
					v.Truncated = true
				case "line_ceiling":
					v.EndLine = 512
				case "nul_manifest":
					v.Content = "module example/package\x00"
				case "oversized":
					v.Content = strings.Repeat("x", 65537)
				}
				return v, nil
			})
			if change == "unavailable" {
				s.Files = nil
			}
			r := ProjectTokensRequest{Repo: api.GraphRepositorySelector{ID: 101}}
			if change == "invalid_scope" {
				r.Repo.ID = 202
			}
			got, err := s.ProjectNameTokens(ctx, principalFor(101), r)
			bestEffort := change == "missing" || change == "unavailable" || change == "truncated" || change == "oversized" || change == "line_ceiling" || change == "nul_manifest"
			if bestEffort {
				if err != nil || !reflect.DeepEqual(got.Tokens, []string{"visible"}) {
					t.Fatalf("best effort %+v %v", got, err)
				}
			} else if err == nil || len(got.Tokens) > 0 || len(got.Generations) > 0 {
				t.Fatalf("authority leaked %+v %v", got, err)
			}
			if change == "invalid_scope" && calls != 0 {
				t.Fatal("read before authorization")
			}
		})
	}
}
