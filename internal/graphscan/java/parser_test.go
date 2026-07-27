package java

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphscan"
)

func TestParseJavaEmitsPackageImportsDeclarationsAndCalls(t *testing.T) {
	got := parseFixture(t, "Service.java")
	if got.Module != "example.service" || got.Language != graphscan.Java ||
		!hasImport(got, "example.base.Parent", "Parent") ||
		!hasImport(got, "example.api", "*") ||
		!hasDeclaration(got, "example.service.Service", "Class") ||
		!hasDeclaration(got, "example.service.Service.run", "Method") ||
		!hasCall(got, "run", "example.service.Service.run", "Service.run", "run", "this.run") ||
		!hasCall(got, "work", "work", "worker.work") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseJavaEmitsExactHeritageKinds(t *testing.T) {
	got := parseFixture(t, "Service.java")
	if !hasDeclaration(got, "example.service.Runnable", "Interface") ||
		!hasHeritage(got, "example.service.Service", graphartifact.EdgeExtends, "example.base.Parent", "Parent") ||
		!hasHeritage(got, "example.service.Service", graphartifact.EdgeImplements, "example.service.Runnable", "Runnable") ||
		hasHeritage(got, "example.service.Runnable", graphartifact.EdgeImplements, "Service") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseJavaPreservesUTF8ByteColumns(t *testing.T) {
	got, err := Parse(t.Context(), "Café.java", []byte("class Café {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got.Declarations, func(value graphscan.Declaration) bool {
		return value.Name == "Café" && value.Range.End.Column == 11
	}) {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseJavaEmitsInterfaceInheritance(t *testing.T) {
	got, err := Parse(t.Context(), "Types.java", []byte(`
package example;
import java.io.Serializable;
interface Base {}
interface Child extends Base, Serializable {}`))
	if err != nil {
		t.Fatal(err)
	}
	if !hasHeritage(got, "example.Child", graphartifact.EdgeExtends, "example.Base", "Base") ||
		!hasHeritage(got, "example.Child", graphartifact.EdgeExtends, "java.io.Serializable", "Serializable") ||
		hasHeritage(got, "example.Child", graphartifact.EdgeImplements, "example.Base", "Base") {
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
