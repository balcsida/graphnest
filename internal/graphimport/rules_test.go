package graphimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"testing"
)

type ruleFixture struct {
	version, file, copy string
}

var ruleFixtures = []ruleFixture{
	{"1.6.0", "../../test/fixtures/codegraph/producer-rules.json", "codegraph-rules-1.6.0.json"},
	{"1.6.2", "../../test/fixtures/codegraph-1.6.2/producer-rules.json", "codegraph-rules-1.6.2.json"},
}

type capturedRules struct {
	ExtensionMap          map[string]string `json:"extension_map"`
	SourceFileDecisions   map[string]bool   `json:"source_file_decisions"`
	DefaultIgnorePatterns []string          `json:"default_ignore_patterns"`
	IgnoreCase            bool              `json:"ignore_case"`
	IgnoreDecisions       map[string]bool   `json:"ignore_decisions"`
	ContentHashVectors    []struct {
		BytesHex   string   `json:"bytes_hex"`
		SHA256     string   `json:"sha256"`
		CodePoints []string `json:"code_points"`
	} `json:"content_hash_vectors"`
}

func loadCaptured(t *testing.T, f ruleFixture) capturedRules {
	t.Helper()
	data, err := os.ReadFile(f.file)
	if err != nil {
		t.Fatal(err)
	}
	var c capturedRules
	if err = json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEmbeddedRulesMatchFixtures(t *testing.T) {
	for _, f := range ruleFixtures {
		want, err := os.ReadFile(f.file)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(f.copy)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s (%v)", f.copy, f.file, err)
		}
	}
}

func TestRulesFor(t *testing.T) {
	r160, err := RulesFor("1.6.0")
	if err != nil || r160.OversizeStamp || r160.MaxSourceFileSize != 1048576 || len(r160.ExtensionMap) < 70 || !r160.IgnoreCase {
		t.Fatalf("1.6.0: %+v %v", r160, err)
	}
	r162, err := RulesFor("1.6.2")
	if err != nil || !r162.OversizeStamp || r162.MaxSourceFileSize != 1048576 {
		t.Fatalf("1.6.2: %+v %v", r162, err)
	}
	r161, err := RulesFor("1.6.1")
	if err != nil || r161.Version != r162.Version || !slices.Equal(r161.DefaultIgnorePatterns, r162.DefaultIgnorePatterns) {
		t.Fatalf("1.6.1: %+v %v", r161, err)
	}
	if len(r162.DefaultIgnorePatterns) != len(r160.DefaultIgnorePatterns)+3 {
		t.Fatalf("1.6.2 adds three build negations: %d vs %d", len(r162.DefaultIgnorePatterns), len(r160.DefaultIgnorePatterns))
	}
	if _, err = RulesFor("9.9.9"); !errors.Is(err, ErrUnknownProducer) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestContentHashVectors(t *testing.T) {
	for _, f := range ruleFixtures {
		rules, err := RulesFor(f.version)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range loadCaptured(t, f).ContentHashVectors {
			input, err := hex.DecodeString(v.BytesHex)
			if err != nil {
				t.Fatal(err)
			}
			var decoded bytes.Buffer
			if err = decodeUTF8(&decoded, bytes.NewReader(input)); err != nil {
				t.Fatal(err)
			}
			var points []string
			for _, r := range decoded.String() {
				points = append(points, strconv.FormatInt(int64(r), 16))
			}
			if !slices.Equal(points, v.CodePoints) {
				t.Errorf("%s %q: code points %v, want %v", f.version, v.BytesHex, points, v.CodePoints)
			}
			got, err := ContentHash(rules, int64(len(input)), bytes.NewReader(input))
			if err != nil || got != v.SHA256 {
				t.Errorf("%s %q: hash %s (%v), want %s", f.version, v.BytesHex, got, err, v.SHA256)
			}
		}
	}
}

func TestContentHashOversize(t *testing.T) {
	r160, _ := RulesFor("1.6.0")
	r162, _ := RulesFor("1.6.2")
	stamp := sha256.Sum256([]byte("codegraph:oversize:1048577"))
	got, err := ContentHash(r162, 1048577, errReader{})
	if err != nil || got != hex.EncodeToString(stamp[:]) {
		t.Fatalf("stamp: %s %v", got, err)
	}
	content := bytes.Repeat([]byte("x"), 1048577)
	plain := sha256.Sum256(content)
	if got, err = ContentHash(r160, 1048577, bytes.NewReader(content)); err != nil || got != hex.EncodeToString(plain[:]) {
		t.Fatalf("1.6.0 hashes content: %s %v", got, err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("oversize content must not be read") }

func TestIsSourceFileDecisions(t *testing.T) {
	for _, f := range ruleFixtures {
		rules, _ := RulesFor(f.version)
		for path, want := range loadCaptured(t, f).SourceFileDecisions {
			if got := IsSourceFile(rules, path, nil); got != want {
				t.Errorf("%s %s: %v, want %v", f.version, path, got, want)
			}
		}
		if IsSourceFile(rules, "a.dota_lua", nil) || !IsSourceFile(rules, "a.dota_lua", map[string]string{".dota_lua": "lua"}) {
			t.Errorf("%s: overrides", f.version)
		}
	}
}

func TestExecGitIgnoredDecisions(t *testing.T) {
	for _, f := range ruleFixtures {
		c := loadCaptured(t, f)
		var paths []string
		for p := range c.IgnoreDecisions {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		ignored, err := ExecGit{}.Ignored(context.Background(), joinLines(c.DefaultIgnorePatterns), c.IgnoreCase, paths)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			if got := slices.Contains(ignored, p); got != c.IgnoreDecisions[p] {
				t.Errorf("%s %s: ignored %v, producer %v", f.version, p, got, c.IgnoreDecisions[p])
			}
		}
	}
}

func joinLines(lines []string) string {
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
