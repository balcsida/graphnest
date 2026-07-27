package javascript

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphscan"
)

func TestParseTypeScriptEmitsImportsDeclarationsAndHeritage(t *testing.T) {
	got := parseFixture(t, "service.ts")
	if got.Language != graphscan.TypeScript ||
		!hasImport(got, "./base", "Parent") ||
		!hasImport(got, "./base", "work") ||
		!hasDeclaration(got, "helper", "Function") ||
		!hasDeclaration(got, "Service", "Class") ||
		!hasDeclaration(got, "Service.run", "Method") ||
		!hasHeritage(got, "Service", graphartifact.EdgeExtends, "Parent") ||
		!hasHeritage(got, "Service", graphartifact.EdgeImplements, "Runnable") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseJavaScriptEmitsExportsArrowsAndCalls(t *testing.T) {
	got := parseFixture(t, "module.js")
	if got.Language != graphscan.JavaScript ||
		!hasDeclaration(got, "defaultExport", "Function") ||
		!hasDeclaration(got, "named", "Function") ||
		!hasCall(got, "named", "named") ||
		!hasCall(got, "work", "work", "doWork") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseTSXUsesTypeScriptGrammar(t *testing.T) {
	got := parseFixture(t, "component.tsx")
	if got.Language != graphscan.TypeScript || !hasDeclaration(got, "View", "Function") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseTypeScriptPreservesUTF8ByteColumns(t *testing.T) {
	got, err := Parse(t.Context(), "utf8.ts", []byte("const café = () => {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range got.Declarations {
		if declaration.Name == "café" && declaration.Range.End.Column == 11 {
			return
		}
	}
	t.Fatalf("Parse() = %#v", got)
}

func parseFixture(t *testing.T, name string) graphscan.File {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(t.Context(), name, source)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func hasDeclaration(file graphscan.File, qualified, kind string) bool {
	return slices.ContainsFunc(file.Declarations, func(value graphscan.Declaration) bool {
		return value.QualifiedName == qualified && value.Kind == kind
	})
}

func hasImport(file graphscan.File, target, alias string) bool {
	return slices.ContainsFunc(file.Imports, func(value graphscan.Import) bool {
		return value.Target == target && value.Alias == alias
	})
}

func hasCall(file graphscan.File, name string, candidates ...string) bool {
	return slices.ContainsFunc(file.References, func(value graphscan.Reference) bool {
		return value.Call && value.Name == name && slices.Equal(value.Candidates, candidates)
	})
}

func hasHeritage(file graphscan.File, child string, kind graphartifact.EdgeKind, candidates ...string) bool {
	return slices.ContainsFunc(file.Heritage, func(value graphscan.Heritage) bool {
		return value.ChildLocalID == child && value.Kind == kind && slices.Equal(value.Candidates, candidates)
	})
}
