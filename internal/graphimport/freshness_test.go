package graphimport

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
)

const fixtureSource = "../../test/fixtures/codegraph/source"

// fakeGit serves an in-memory archive and delegates ignore evaluation to git.
type fakeGit struct {
	archive      []byte
	archiveCalls int
	symlinks     []string
}

func (g *fakeGit) Archive(context.Context, string, string) (io.ReadCloser, error) {
	g.archiveCalls++
	return io.NopCloser(bytes.NewReader(g.archive)), nil
}

func (g *fakeGit) Ignored(ctx context.Context, patterns string, ignoreCase bool, paths []string) ([]string, error) {
	return ExecGit{}.Ignored(ctx, patterns, ignoreCase, paths)
}

func fixtureTree(t *testing.T) map[string][]byte {
	t.Helper()
	tree := map[string][]byte{}
	err := filepath.WalkDir(fixtureSource, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(fixtureSource, path)
		tree[filepath.ToSlash(rel)] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// tarOf builds a git-archive-like stream: pax global header, directory entries, then the files.
func (g *fakeGit) withTree(t *testing.T, tree map[string][]byte) *fakeGit {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": strings.Repeat("a", 40)}}))
	dirs := map[string]bool{}
	names := slices.Sorted(maps.Keys(tree))
	for _, name := range names {
		for dir := filepath.Dir(name); dir != "."; dir = filepath.Dir(dir) {
			dirs[dir+"/"] = true
		}
	}
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		must(w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: dir, Mode: 0o755}))
	}
	for _, name := range names {
		must(w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(tree[name])), Mode: 0o644}))
		_, err := w.Write(tree[name])
		must(err)
	}
	for _, name := range g.symlinks {
		must(w.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: "elsewhere"}))
	}
	must(w.Close())
	g.archive = buf.Bytes()
	return g
}

func with(tree map[string][]byte, edits map[string]string) map[string][]byte {
	out := maps.Clone(tree)
	for name, content := range edits {
		if content == "\x00delete" {
			delete(out, name)
		} else {
			out[name] = []byte(content)
		}
	}
	return out
}

func readFixture(t *testing.T, path string) *Snapshot {
	t.Helper()
	s, err := Read(context.Background(), path, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func verify(t *testing.T, g Git, s *Snapshot) *Freshness {
	t.Helper()
	f, err := Verify(context.Background(), g, "repo", "abc", s, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestVerifyFresh(t *testing.T) {
	tree := fixtureTree(t)
	for _, path := range []string{fixturePath, fixturePath162} {
		f := verify(t, (&fakeGit{}).withTree(t, tree), readFixture(t, path))
		if f.Status != StatusFresh || f.Compared != 13 || len(f.Modified)+len(f.NotInCommit)+len(f.NotIndexed)+len(f.Unverified) != 0 || f.Detail == "" {
			t.Errorf("%s: %+v", path, f)
		}
	}
}

func TestVerifyDifferences(t *testing.T) {
	tree := fixtureTree(t)
	core := string(tree["core.ts"])
	cases := []struct {
		name    string
		edits   map[string]string
		status  string
		check   func(*Freshness) bool
		compare int
	}{
		{"modified", map[string]string{"core.ts": core + " "}, StatusStale, func(f *Freshness) bool { return slices.Equal(f.Modified, []string{"core.ts"}) }, 13},
		{"missing", map[string]string{"Model.java": "\x00delete"}, StatusStale, func(f *Freshness) bool { return slices.Equal(f.NotInCommit, []string{"Model.java"}) }, 12},
		{"extra source", map[string]string{"extra.ts": "export {}"}, StatusStale, func(f *Freshness) bool { return slices.Equal(f.NotIndexed, []string{"extra.ts"}) }, 13},
		{"ignored and data dirs", map[string]string{"node_modules/x.ts": "x", "Dist/x.ts": "x", ".codegraph/codegraph.db": "x", ".git/x.ts": "x", "pkg/.codegraph-1/x.ts": "x"}, StatusFresh, nil, 13},
		{"shopify json", map[string]string{"templates/a.json": "{}"}, StatusStale, func(f *Freshness) bool { return slices.Equal(f.NotIndexed, []string{"templates/a.json"}) }, 13},
		{"plain json", map[string]string{"config/a.json": "{}"}, StatusFresh, nil, 13},
		{"extension override", map[string]string{"codegraph.json": `{"exclude":["excluded.ts"],"extensions":{".dota_lua":"lua"}}`, "a.dota_lua": "x"}, StatusStale, func(f *Freshness) bool { return slices.Equal(f.NotIndexed, []string{"a.dota_lua"}) }, 13},
		{"no override", map[string]string{"a.dota_lua": "x"}, StatusFresh, nil, 13},
		{"include cannot resurface a default", map[string]string{"codegraph.json": `{"exclude":["excluded.ts"],"include":["vendor/"]}`, "vendor/a.go": "package a"}, StatusFresh, nil, 13},
		{"include re-admits gitignored", map[string]string{".gitignore": "lib/\n", "codegraph.json": `{"exclude":["excluded.ts"],"include":["lib/"]}`, "lib/a.go": "package a"}, StatusStale, func(f *Freshness) bool { return slices.Equal(f.NotIndexed, []string{"lib/a.go"}) }, 13},
		{"gitignored", map[string]string{".gitignore": "lib/\n", "lib/a.go": "package a"}, StatusFresh, nil, 13},
		{"exclude beats include", map[string]string{".gitignore": "lib/\n", "codegraph.json": `{"include":["lib/"],"exclude":["lib/b/","excluded.ts"]}`, "lib/b/a.go": "package a"}, StatusFresh, nil, 13},
		{"malformed config", map[string]string{"codegraph.json": `{`}, StatusStale, func(f *Freshness) bool {
			return slices.Equal(f.NotIndexed, []string{"excluded.ts"}) && strings.Contains(f.Detail, "not valid JSON")
		}, 13},
	}
	for _, c := range cases {
		f := verify(t, (&fakeGit{}).withTree(t, with(tree, c.edits)), readFixture(t, fixturePath))
		if f.Status != c.status || f.Compared != c.compare || (c.check != nil && !c.check(f)) {
			t.Errorf("%s: %+v", c.name, f)
		}
		if c.status == StatusFresh && len(f.NotIndexed)+len(f.Modified)+len(f.NotInCommit) != 0 {
			t.Errorf("%s: unexpected gaps %+v", c.name, f)
		}
	}
}

func TestVerifyExcludeRemovesGapButIndexedFileIsStillCompared(t *testing.T) {
	tree := fixtureTree(t)
	s := readFixture(t, fixturePath)
	if f := verify(t, (&fakeGit{}).withTree(t, tree), s); f.Compared != 13 || f.Status != StatusFresh {
		t.Fatalf("baseline: %+v", f)
	}
	s.Files = slices.DeleteFunc(s.Files, func(f File) bool { return f.Path == "app/index.tsx" })
	if f := verify(t, (&fakeGit{}).withTree(t, tree), s); !slices.Equal(f.NotIndexed, []string{"app/index.tsx"}) {
		t.Fatalf("not indexed: %+v", f)
	}
	excluded := with(tree, map[string]string{"codegraph.json": `{"exclude":["excluded.ts","app/"]}`})
	if f := verify(t, (&fakeGit{}).withTree(t, excluded), s); f.Status != StatusFresh || len(f.NotIndexed) != 0 {
		t.Fatalf("excluded: %+v", f)
	}
	if f := verify(t, (&fakeGit{}).withTree(t, excluded), readFixture(t, fixturePath)); f.Status != StatusFresh || f.Compared != 13 {
		t.Fatalf("indexed but excluded: %+v", f)
	}
}

func TestVerifySymlinkIsUnverifiable(t *testing.T) {
	tree := with(fixtureTree(t), map[string]string{"core.ts": "\x00delete"})
	g := &fakeGit{symlinks: []string{"core.ts"}}
	f := verify(t, g.withTree(t, tree), readFixture(t, fixturePath))
	if f.Status != StatusUnverifiable || !slices.Equal(f.Unverified, []string{"core.ts"}) || len(f.NotInCommit) != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestVerifyUnknownProducerDoesNotTouchGit(t *testing.T) {
	g := &fakeGit{}
	s := readFixture(t, fixturePath)
	for i := range s.Metadata {
		if s.Metadata[i].Key == "indexed_with_version" {
			s.Metadata[i].Value = "9.9.9"
		}
	}
	f := verify(t, g, s)
	if f.Status != StatusUnverifiable || g.archiveCalls != 0 || !strings.Contains(f.Detail, "9.9.9") {
		t.Fatalf("%+v calls=%d", f, g.archiveCalls)
	}
	s.Metadata = nil
	if f = verify(t, g, s); f.Status != StatusUnverifiable || g.archiveCalls != 0 {
		t.Fatalf("no version: %+v", f)
	}
}

func TestVerifyOversizeAndVirtual(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 1048577)
	stamp := sha256.Sum256([]byte("codegraph:oversize:1048577"))
	content := sha256.Sum256(big)
	snapshot := func(version string, hash [32]byte) *Snapshot {
		return &Snapshot{
			Metadata: []MetadataEntry{{Key: "indexed_with_version", Value: version}},
			Files:    []File{{Path: "big.ts", ContentHash: hex.EncodeToString(hash[:])}},
			Nodes:    []Node{{ID: "n", FilePath: "big.ts"}, {ID: "v", FilePath: "virtual:ext"}},
		}
	}
	tree := map[string][]byte{"big.ts": big}
	if f := verify(t, (&fakeGit{}).withTree(t, tree), snapshot("1.6.2", stamp)); f.Status != StatusFresh || f.Virtual != 1 {
		t.Fatalf("1.6.2 stamp: %+v", f)
	}
	if f := verify(t, (&fakeGit{}).withTree(t, tree), snapshot("1.6.0", content)); f.Status != StatusFresh {
		t.Fatalf("1.6.0 content: %+v", f)
	}
	if f := verify(t, (&fakeGit{}).withTree(t, tree), snapshot("1.6.0", stamp)); f.Status != StatusStale {
		t.Fatalf("1.6.0 stamp: %+v", f)
	}
}

func TestVerifyUnicodeFileHashesLikeProducer(t *testing.T) {
	tree := fixtureTree(t)
	if !bytes.Contains(tree["unicode.ts"], []byte("\r\n")) {
		t.Fatal("unicode.ts lost its CRLF")
	}
	f := verify(t, (&fakeGit{}).withTree(t, map[string][]byte{"unicode.ts": tree["unicode.ts"]}), &Snapshot{
		Metadata: []MetadataEntry{{Key: "indexed_with_version", Value: "1.6.0"}},
		Files:    readFixture(t, fixturePath).Files[:0],
	})
	if f.Compared != 0 || !slices.Equal(f.NotIndexed, []string{"unicode.ts"}) {
		t.Fatalf("setup: %+v", f)
	}
	for _, file := range readFixture(t, fixturePath).Files {
		if file.Path == "unicode.ts" {
			g := (&fakeGit{}).withTree(t, map[string][]byte{"unicode.ts": tree["unicode.ts"]})
			s := &Snapshot{Metadata: []MetadataEntry{{Key: "indexed_with_version", Value: "1.6.0"}}, Files: []File{file}}
			if f = verify(t, g, s); f.Status != StatusFresh || f.Compared != 1 {
				t.Fatalf("unicode.ts: %+v", f)
			}
			return
		}
	}
	t.Fatal("unicode.ts not indexed")
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=GraphNest Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=GraphNest Test", "GIT_COMMITTER_EMAIL=test@example.invalid", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestVerifyWithExecGit(t *testing.T) {
	repo := t.TempDir()
	for name, data := range fixtureTree(t) {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "fixture")
	first := gitIn(t, repo, "rev-parse", "HEAD")
	s := readFixture(t, fixturePath)
	ctx := context.Background()

	f, err := Verify(ctx, ExecGit{}, repo, first, s, VerifyOptions{})
	if err != nil || f.Status != StatusFresh || f.Compared != 13 {
		t.Fatalf("fresh: %+v %v", f, err)
	}
	if err = os.WriteFile(filepath.Join(repo, "core.ts"), []byte("// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "commit", "-q", "-am", "change")
	second := gitIn(t, repo, "rev-parse", "HEAD")
	if f, err = Verify(ctx, ExecGit{}, repo, second, s, VerifyOptions{}); err != nil || f.Status != StatusStale || !slices.Equal(f.Modified, []string{"core.ts"}) {
		t.Fatalf("stale: %+v %v", f, err)
	}
	if f, err = Verify(ctx, ExecGit{}, repo, first, s, VerifyOptions{}); err != nil || f.Status != StatusFresh {
		t.Fatalf("old commit still fresh: %+v %v", f, err)
	}
	if _, err = Verify(ctx, ExecGit{}, repo, "no-such-ref", s, VerifyOptions{}); err == nil || !strings.Contains(err.Error(), "not a valid object name") {
		t.Fatalf("unknown commit: %v", err)
	}
}
