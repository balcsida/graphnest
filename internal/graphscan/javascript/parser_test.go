package javascript

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestParseJavaScriptEmitsDefaultArrowDeclaration(t *testing.T) {
	got := parseFixture(t, "default-arrow.js")
	if !hasDeclaration(got, "defaultExport", "Function") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseJavaScriptEmitsAnonymousDefaultClass(t *testing.T) {
	got := parseFixture(t, "default-class.js")
	if !hasDeclaration(got, "defaultExport", "Class") || !hasDeclaration(got, "defaultExport.run", "Method") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseJavaScriptEmitsExportClauseAlias(t *testing.T) {
	got := parseFixture(t, "named-export.js")
	if !hasDeclaration(got, "bar", "Function") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseJavaScriptEmitsAllRelativeImportShapes(t *testing.T) {
	got := parseFixture(t, "imports.js")
	if !hasImport(got, "./default.js", "thing") ||
		!hasImport(got, "./namespace.js", "all") ||
		!hasImport(got, "./named.js", "alias") ||
		!hasImport(got, "./setup.js", "") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseTypeScriptMethodCallResolvesToClassMethod(t *testing.T) {
	got := parseFixture(t, "method-call.ts")
	if !hasCall(got, "run", "run", "Service.run", "this.run") ||
		!hasCall(got, "run", "run", "obj.run") {
		t.Fatalf("Parse() = %#v", got)
	}
	artifact, err := graphscan.Resolve(1, strings.Repeat("a", 40), []graphscan.File{got})
	if err != nil || !hasResolvedCall(artifact, "Service.start", "Service.run") {
		t.Fatalf("Resolve() = %#v, %v", artifact, err)
	}
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

func hasResolvedCall(artifact graphartifact.Artifact, source, target string) bool {
	names := map[string]string{}
	for _, node := range artifact.Nodes {
		names[node.UID] = node.QualifiedName
	}
	return slices.ContainsFunc(artifact.Edges, func(edge graphartifact.Edge) bool {
		return edge.Kind == graphartifact.EdgeCalls && names[edge.SourceUID] == source && names[edge.TargetUID] == target
	})
}
