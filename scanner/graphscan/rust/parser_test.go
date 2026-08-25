package rust

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/scanner/graphscan"
)

func TestParseRustEmitsModulesGroupedUsesFunctionsAndCalls(t *testing.T) {
	got := parseFixture(t, "lib.rs")
	if got.Module != "lib" || got.Language != graphscan.Rust ||
		!hasImport(got, "crate::jobs::Job", "Job") ||
		!hasImport(got, "crate::jobs::Queue", "WorkQueue") ||
		!hasDeclaration(got, "lib::service", "Module") ||
		!hasDeclaration(got, "lib::service::start", "Function") ||
		!hasDeclaration(got, "lib::service::Job::new", "Method") ||
		!hasCall(got, "start", "lib::service::start", "start") ||
		!hasCall(got, "run", "run", "job.run") ||
		hasCallCandidate(got, "run", "lib::service::Job::run") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseRustEmitsTraitAndImplementationEvidence(t *testing.T) {
	got := parseFixture(t, "lib.rs")
	if !hasDeclaration(got, "lib::service::Run", "Trait") ||
		!hasDeclaration(got, "lib::service::Job", "Struct") ||
		!hasHeritage(got, "lib::service::Job", graphartifact.EdgeImplements, "lib::service::Run", "Run") ||
		hasHeritage(got, "lib::service::Job", graphartifact.EdgeImplements, "Job") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseRustPreservesUTF8ByteColumns(t *testing.T) {
	got, err := Parse(t.Context(), "lib.rs", []byte("fn café() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got.Declarations, func(value graphscan.Declaration) bool {
		return value.Name == "café" && value.Range.End.Column == 8
	}) {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseRustEmitsTraitMethods(t *testing.T) {
	got, err := Parse(t.Context(), "lib.rs", []byte("trait Run { fn run(&self); }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasDeclaration(got, "lib::Run::run", "Method") ||
		hasDeclaration(got, "lib::run", "Function") {
		t.Fatalf("Parse() = %#v", got)
	}
}

func TestParseRustEmitsExternalModules(t *testing.T) {
	got, err := Parse(t.Context(), "lib.rs", []byte("mod worker;\nmod inline {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasDeclaration(got, "lib::worker", "Module") ||
		!hasDeclaration(got, "lib::inline", "Module") {
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

func hasCallCandidate(file graphscan.File, name, candidate string) bool {
	return slices.ContainsFunc(file.References, func(value graphscan.Reference) bool {
		return value.Call && value.Name == name && slices.Contains(value.Candidates, candidate)
	})
}

func hasHeritage(file graphscan.File, child string, kind graphartifact.EdgeKind, candidates ...string) bool {
	return slices.ContainsFunc(file.Heritage, func(value graphscan.Heritage) bool {
		return value.ChildLocalID == child && value.Kind == kind && slices.Equal(value.Candidates, candidates)
	})
}
