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
