package kotlin

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/scanner/graphscan"
)

func TestParseKotlinEmitsPackageAliasesDeclarationsAndCalls(t *testing.T) {
	got := parseFixture(t, "service.kt")
	if got.Module != "example.service" || got.Language != graphscan.Kotlin ||
		!hasImport(got, "example.base.Parent", "Base") ||
		!hasImport(got, "example.api", "*") ||
		!hasDeclaration(got, "example.service.helper", "Function") ||
		!hasDeclaration(got, "example.service.Service", "Class") ||
		!hasDeclaration(got, "example.service.Singleton", "Object") ||
		!hasDeclaration(got, "example.service.Service.run", "Method") ||
		!hasCall(got, "run", "example.service.Service.run", "Service.run", "run", "this.run") ||
		!hasCall(got, "helper", "example.service.helper", "helper") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseKotlinSeparatesInheritanceFromImplementation(t *testing.T) {
	got := parseFixture(t, "service.kt")
	if !hasDeclaration(got, "example.service.Worker", "Interface") ||
		!hasHeritage(got, "example.service.Service", graphartifact.EdgeExtends, "example.base.Parent", "Base") ||
		!hasHeritage(got, "example.service.Service", graphartifact.EdgeImplements, "example.service.Worker", "Worker") ||
		hasHeritage(got, "example.service.Service", graphartifact.EdgeImplements, "example.base.Parent", "Base") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseKotlinPreservesUTF8ByteColumns(t *testing.T) {
	got, err := Parse(t.Context(), "utf8.kt", []byte("fun café() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got.Declarations, func(value graphscan.Declaration) bool {
		return value.Name == "café" && value.Range.End.Column == 9
	}) {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseKotlinRecognizesModifiedInterfaces(t *testing.T) {
	got, err := Parse(t.Context(), "types.kt", []byte(`
package example
public interface Visible
@Deprecated("legacy")
internal interface Annotated`))
	if err != nil {
		t.Fatal(err)
	}
	if !hasDeclaration(got, "example.Visible", "Interface") ||
		!hasDeclaration(got, "example.Annotated", "Interface") ||
		hasDeclaration(got, "example.Visible", "Class") ||
		hasDeclaration(got, "example.Annotated", "Class") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseKotlinEmitsStableOverloadSignatures(t *testing.T) {
	got, err := Parse(t.Context(), "Overloaded.kt", []byte(`
package example
class Overloaded {
  fun run(value: Int): Unit {}
  fun run(value: String, count: Long): String = value
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !hasSignature(got, "example.Overloaded.run", "(Int):Unit") ||
		!hasSignature(got, "example.Overloaded.run", "(String,Long):String") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func hasSignature(file graphscan.File, qualified, signature string) bool {
	return slices.ContainsFunc(file.Declarations, func(value graphscan.Declaration) bool {
		return value.QualifiedName == qualified && value.Signature == signature
	})
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
