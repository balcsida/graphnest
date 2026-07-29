package graphscan

import (
	"os"
	"path/filepath"
	"testing"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

func TestGrammarMatrix(t *testing.T) {
	for _, fixture := range []string{"smoke.go", "smoke.js", "smoke.ts", "smoke.tsx", "Smoke.java", "smoke.kt", "smoke.rs"} {
		t.Run(fixture, func(t *testing.T) {
			source := readFixture(t, fixture)
			language, ok := LanguageForExtension(filepath.Ext(fixture))
			if !ok {
				t.Fatal("missing language")
			}
			parser := tree_sitter.NewParser()
			defer parser.Close()
			if err := parser.SetLanguage(language); err != nil {
				t.Fatal(err)
			}
			tree := parser.Parse(source, nil)
			defer tree.Close()
			if tree.RootNode().HasError() {
				t.Fatalf("parse error: %s", tree.RootNode().ToSexp())
			}
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return source
}
