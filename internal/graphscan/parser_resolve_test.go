package graphscan_test

import (
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphscan"
	"github.com/grepnest/grepnest/internal/graphscan/golang"
	"github.com/grepnest/grepnest/internal/graphscan/java"
)

func TestResolveParsedGoImportAcrossFiles(t *testing.T) {
	files := []graphscan.File{
		parseGo(t, "lib/lib.go", "package lib\nfunc F() {}\n"),
		parseGo(t, "main.go", "package main\nimport \"example.com/project/lib\"\nfunc main() { lib.F() }\n"),
	}
	artifact, err := graphscan.Resolve(101, strings.Repeat("a", 40), files)
	if err != nil || !hasEdge(artifact, graphartifact.EdgeCalls, "main.main", "lib.F") ||
		!hasEdge(artifact, graphartifact.EdgeImports, "main.go", "lib/lib.go") {
		t.Fatalf("Resolve() = %#v, %v", artifact, err)
	}
}

func TestResolveParsedGoImplicitInterfaceAcrossFiles(t *testing.T) {
	files := []graphscan.File{
		parseGo(t, "base.go", "package worker\ntype Base interface { Close() error }\n"),
		parseGo(t, "runner.go", "package worker\ntype Runner interface { Base; Run() error }\n"),
		parseGo(t, "job.go", "package worker\ntype Job struct{}\n"),
		parseGo(t, "methods.go", "package worker\nfunc (Job) Run() error { return nil }\nfunc (Job) Close() error { return nil }\n"),
	}
	artifact, err := graphscan.Resolve(101, strings.Repeat("a", 40), files)
	if err != nil || !hasEdge(artifact, graphartifact.EdgeImplements, "Job", "Runner") ||
		!hasEdge(artifact, graphartifact.EdgeImplements, "Job", "Base") {
		t.Fatalf("Resolve() = %#v, %v", artifact, err)
	}
}

func TestResolveParsedGoImplicitInterfaceRespectsPackageAndReceiver(t *testing.T) {
	files := []graphscan.File{
		parseGo(t, "api/runner.go", "package api\ntype Runner interface { Run() error }\n"),
		parseGo(t, "worker/job.go", "package worker\ntype Job struct{}\nfunc (Job) Run() error { return nil }\n"),
		parseGo(t, "api/pointer.go", "package api\ntype Pointer struct{}\nfunc (*Pointer) Run() error { return nil }\n"),
	}
	artifact, err := graphscan.Resolve(101, strings.Repeat("a", 40), files)
	if err != nil {
		t.Fatal(err)
	}
	if hasEdge(artifact, graphartifact.EdgeImplements, "Job", "Runner") ||
		hasEdge(artifact, graphartifact.EdgeImplements, "Pointer", "Runner") {
		t.Fatalf("Resolve() inferred an invalid implementation: %#v", artifact)
	}
}

func TestResolveParsedGoEmbeddedOnlyInterfaceAcrossFiles(t *testing.T) {
	files := []graphscan.File{
		parseGo(t, "base.go", "package worker\ntype Base interface { Close() error }\n"),
		parseGo(t, "worker.go", "package worker\ntype Worker interface { Base }\n"),
		parseGo(t, "job.go", "package worker\ntype Job struct{}\n"),
		parseGo(t, "close.go", "package worker\nfunc (Job) Close() error { return nil }\n"),
	}
	artifact, err := graphscan.Resolve(101, strings.Repeat("a", 40), files)
	if err != nil || !hasEdge(artifact, graphartifact.EdgeImplements, "Job", "Worker") {
		t.Fatalf("Resolve() = %#v, %v", artifact, err)
	}
}

func TestResolveParsedJavaImportAcrossFiles(t *testing.T) {
	files := []graphscan.File{
		parseJava(t, "src/lib/Helper.java", "package lib; public class Helper { public static void run() {} }\n"),
		parseJava(t, "src/app/Main.java", "package app; import lib.Helper; class Main { void call() { Helper.run(); } }\n"),
	}
	artifact, err := graphscan.Resolve(101, strings.Repeat("a", 40), files)
	if err != nil || !hasEdge(artifact, graphartifact.EdgeCalls, "app.Main.call", "lib.Helper.run") ||
		!hasEdge(artifact, graphartifact.EdgeImports, "src/app/Main.java", "src/lib/Helper.java") {
		t.Fatalf("Resolve() = %#v, %v", artifact, err)
	}
}

func parseGo(t *testing.T, path, source string) graphscan.File {
	t.Helper()
	file, err := golang.Parse(t.Context(), path, []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func parseJava(t *testing.T, path, source string) graphscan.File {
	t.Helper()
	file, err := java.Parse(t.Context(), path, []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func hasEdge(artifact graphartifact.Artifact, kind graphartifact.EdgeKind, source, target string) bool {
	for _, edge := range artifact.Edges {
		if edge.Kind == kind && nodeName(artifact, edge.SourceUID) == source && nodeName(artifact, edge.TargetUID) == target {
			return true
		}
	}
	return false
}

func nodeName(artifact graphartifact.Artifact, uid string) string {
	for _, node := range artifact.Nodes {
		if node.UID != uid {
			continue
		}
		if node.Kind == graphartifact.NodeFile {
			return node.Path
		}
		if node.Kind == graphartifact.NodeSymbol {
			return node.QualifiedName
		}
	}
	return uid
}
