package golang

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/scanner/graphscan"
)

func TestParseGoEmitsPackageImportsAndDeclarations(t *testing.T) {
	got := parseFixture(t, "service.go")
	if got.Module != "service" || got.Language != graphscan.Go ||
		!hasImport(got, "example.com/log", "logging") ||
		!hasDeclaration(got, "service.Start", "Function") ||
		!hasDeclaration(got, "Runner.Run", "Method") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseGoEmitsCallsAndInterfaceEvidence(t *testing.T) {
	got := parseFixture(t, "service.go")
	if !hasCall(got, "Run", "Runner.Run", "Run") ||
		!hasInterfaceMethod(got, "Worker", "Run") ||
		!hasValueMethod(got, "Runner", "Run") ||
		!hasHeritage(got, "Worker", graphartifact.EdgeExtends, "Base") ||
		hasInterfaceMethod(got, "Partial", "Run") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseGoPreservesUTF8ByteColumns(t *testing.T) {
	got, err := Parse(t.Context(), "utf8.go", []byte("package p\nfunc café() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range got.Declarations {
		if declaration.Name == "café" && declaration.Range.End.Column == 10 {
			return
		}
	}
	t.Fatalf("Parse() = %#v", got)
}

func TestParseGoRejectsImplicitInterfaceSignatureMismatch(t *testing.T) {
	got := parseFixture(t, "interface-mismatch.go")
	if hasHeritage(got, "Wrong", graphartifact.EdgeImplements, "Runner") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseGoExcludesPointerMethodsFromValueMethodSet(t *testing.T) {
	got := parseFixture(t, "interface-pointer.go")
	if hasHeritage(got, "PointerOnly", graphartifact.EdgeImplements, "Closer") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseGoOrdersPackageQualifiedCallCandidate(t *testing.T) {
	got, err := Parse(t.Context(), "main.go", []byte("package main\nimport \"example.com/lib\"\nfunc main() { lib.Run() }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasCall(got, "Run", "lib.Run", "Run") {
		t.Fatalf("Parse() = %#v", got)
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

func hasInterfaceMethod(file graphscan.File, typeName, method string) bool {
	return slices.ContainsFunc(file.Declarations, func(value graphscan.Declaration) bool {
		return value.TypeName == typeName && value.Name == method
	})
}

func hasValueMethod(file graphscan.File, receiver, method string) bool {
	return slices.ContainsFunc(file.Declarations, func(value graphscan.Declaration) bool {
		return value.Receiver == receiver && value.Name == method && !value.PointerReceiver
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
