//go:build integration

package postgres

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
)

// TestGraphSymbolToolsMatchCodeGraph compares name-addressed callers, callees
// and impact with the pinned CodeGraph MCP handlers' real answers. The
// renderer below reproduces only upstream's markdown layout, so equal text
// means equal definitions, grouping, neighbors, order and labels.
func TestGraphSymbolToolsMatchCodeGraph(t *testing.T) {
	path := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if path == "" {
		t.Skip("set GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE to the exported real oracle artifact")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	s, id := readyGraphStore(t, a.Commit)
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "symbol-oracle"}, a); err != nil {
		t.Fatal(err)
	}
	oracleData, err := os.ReadFile("../../test/fixtures/codegraph/library-expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle map[string]json.RawMessage
	if err := json.Unmarshal(oracleData, &oracle); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	for _, test := range []struct {
		id, tool, symbol, file string
		limit, depth           int
		// Impact walks shortest dependency depth (docs/graph-analysis.md), so
		// it also reaches nodes the pinned depth-first walk omits.
		corrected []string
	}{
		{"mcp-callers-grouped", "callers", "normalize", "", 0, 0, nil},
		{"mcp-callers-file", "callers", "normalize", "core.ts", 0, 0, nil},
		{"mcp-callers-file-miss", "callers", "normalize", "missing.ts", 0, 0, nil},
		{"mcp-callers-limit", "callers", "normalize", "core.ts", 1, 0, nil},
		{"mcp-callers-qualified", "callers", "Service.greet", "", 0, 0, nil},
		{"mcp-callers-missing", "callers", "MissingFixtureSymbol987", "", 0, 0, nil},
		{"mcp-callees-single", "callees", "run", "", 0, 0, nil},
		{"mcp-callees-grouped", "callees", "greet", "", 0, 0, nil},
		{"mcp-impact-file", "impact", "normalize", "core.ts", 0, 0, []string{`"normalize"|consumer.ts|consumer.ts:1`, `"normalize"|consumer.ts|processGreeting:2`}},
		{"mcp-impact-depth", "impact", "normalize", "core.ts", 0, 1, nil},
		{"mcp-impact-grouped", "impact", "identity", "", 0, 0, nil},
	} {
		t.Run(test.id, func(t *testing.T) {
			var got string
			if test.tool == "impact" {
				result, err := service.SymbolImpact(t.Context(), graphprotocol.SymbolImpactRequest{Scope: scope, Symbol: test.symbol, File: test.file, Depth: test.depth})
				if err != nil {
					t.Fatal(err)
				}
				got = renderImpact(test.symbol, test.file, result)
			} else {
				direction := map[string]string{"callers": "incoming", "callees": "outgoing"}[test.tool]
				result, err := service.SymbolCalls(t.Context(), graphprotocol.SymbolCallsRequest{Scope: scope, Symbol: test.symbol, File: test.file, Direction: direction, Limit: test.limit})
				if err != nil {
					t.Fatal(err)
				}
				got = renderCalls(test.tool, test.symbol, test.file, result)
			}
			var answer struct {
				Content []struct{ Text string } `json:"content"`
			}
			if err := json.Unmarshal(oracle[test.id], &answer); err != nil || len(answer.Content) == 0 {
				t.Fatalf("oracle %s: %v", test.id, err)
			}
			var want []string
			for _, item := range answer.Content {
				want = append(want, item.Text)
			}
			expected := strings.Join(want, "\n")
			if test.tool == "impact" {
				// Level-by-level order differs from the depth-first order.
				wantSet := append(impactEntries(expected), test.corrected...)
				slices.Sort(wantSet)
				if gotSet := impactEntries(got); !slices.Equal(gotSet, wantSet) {
					t.Fatalf("GraphNest %v\nwant %v\n\nGraphNest:\n%s\n\nCodeGraph:\n%s", gotSet, wantSet, got, expected)
				}
				return
			}
			if canonicalSections(got) != canonicalSections(expected) {
				t.Fatalf("GraphNest:\n%s\n\nCodeGraph:\n%s", got, expected)
			}
		})
	}
}

func renderCalls(tool, symbol, file string, r graphprotocol.SymbolResponse) string {
	if r.Status == graphprotocol.StatusNotFound {
		return fmt.Sprintf("Symbol %q not found in the codebase", symbol)
	}
	title := map[string]string{"callers": "Callers", "callees": "Callees"}[tool]
	filterNote := unmatchedFilterNote(symbol, file, r)
	if len(r.Definitions) == 1 {
		definition := r.Definitions[0]
		note := aggregationNote(symbol, r)
		if len(definition.Related) == 0 {
			return fmt.Sprintf("No %s found for %q", tool, symbol) + note + filterNote
		}
		lines := []string{fmt.Sprintf("**%s of %s (%d found)**", title, symbol, len(definition.Related)), ""}
		for _, related := range definition.Related {
			lines = append(lines, relatedLine(related))
		}
		return strings.Join(lines, "\n") + note + filterNote
	}
	lines := []string{fmt.Sprintf("**%s of %s — %d distinct definitions (narrow with `file`)**", title, symbol, len(r.Definitions))}
	for _, definition := range r.Definitions {
		head := definition.Definitions[0].Fact
		lines = append(lines, "", fmt.Sprintf("**%s** (%s) — %s%s", head.GetQualifiedName(), head.GetKind(), head.GetPath(), lineSuffix(head)))
		if len(definition.Related) == 0 {
			lines = append(lines, fmt.Sprintf("- (no %s)", tool))
		}
		for _, related := range definition.Related {
			lines = append(lines, relatedLine(related))
		}
	}
	return strings.Join(lines, "\n") + filterNote
}

func renderImpact(symbol, file string, r graphprotocol.SymbolResponse) string {
	if r.Status == graphprotocol.StatusNotFound {
		return fmt.Sprintf("Symbol %q not found in the codebase", symbol)
	}
	filterNote := unmatchedFilterNote(symbol, file, r)
	if len(r.Definitions) == 1 {
		return impactSection(symbol, r.Definitions[0]) + aggregationNote(symbol, r) + filterNote
	}
	sections := []string{fmt.Sprintf("**Impact of %s — %d distinct definitions (each with its own blast radius; narrow with `file`)**", symbol, len(r.Definitions))}
	for _, definition := range r.Definitions {
		head := definition.Definitions[0].Fact
		sections = append(sections, "", impactSection(fmt.Sprintf("%s (%s%s)", head.GetQualifiedName(), head.GetPath(), lineSuffix(head)), definition))
	}
	return strings.Join(sections, "\n") + filterNote
}

func impactSection(label string, definition graphprotocol.SymbolDefinition) string {
	lines := []string{fmt.Sprintf("**Impact: %q affects %d symbols**", label, len(definition.Entities)), ""}
	var files []string
	byFile := map[string][]string{}
	for _, entity := range definition.Entities {
		path := entity.Fact.GetPath()
		if _, ok := byFile[path]; !ok {
			files = append(files, path)
		}
		byFile[path] = append(byFile[path], fmt.Sprintf("%s:%d", entity.Fact.GetName(), startLine(entity.Fact)))
	}
	for _, path := range files {
		lines = append(lines, fmt.Sprintf("**%s:**", path), strings.Join(byFile[path], ", "), "")
	}
	return strings.Join(lines, "\n")
}

// aggregationNote mirrors findAllSymbols' note, suppressed when file narrowed.
func aggregationNote(symbol string, r graphprotocol.SymbolResponse) string {
	matches := r.Definitions[0].Definitions
	if len(matches) < 2 || r.FileFilter == "matched" {
		return ""
	}
	var locations []string
	for _, match := range matches {
		locations = append(locations, fmt.Sprintf("%s at %s:%d", match.Fact.GetKind(), match.Fact.GetPath(), startLine(match.Fact)))
	}
	return fmt.Sprintf("\n\n> **Note:** Aggregated results across %d symbols named %q: %s", len(matches), symbol, strings.Join(locations, ", "))
}

func unmatchedFilterNote(symbol, file string, r graphprotocol.SymbolResponse) string {
	if r.FileFilter != "unmatched" {
		return ""
	}
	return fmt.Sprintf("\n\n> **Note:** no definition of %q matches file %q — showing all definitions instead.", symbol, file)
}

func relatedLine(related graphprotocol.SymbolRelated) string {
	node, edge := related.Entity.Fact, related.Edge.Fact
	label := ""
	switch kind := strings.ToLower(strings.TrimPrefix(edge.GetKind().String(), "EDGE_KIND_")); {
	case kind == "calls":
	case callbackRegistration(edge):
		label = "callback registration"
	case kind == "instantiates":
		label = "instantiation"
	case kind == "imports":
		label = "import"
	case kind == "references":
		label = "reference"
	default:
		label = kind
	}
	if label != "" {
		label = " — via " + label
	}
	return fmt.Sprintf("- %s (%s) - %s%s%s", node.GetName(), node.GetKind(), node.GetPath(), lineSuffix(node), label)
}

func callbackRegistration(edge *graphv2.Edge) bool {
	for _, extension := range edge.GetExtensions() {
		var metadata struct {
			FnRef bool `json:"fnRef"`
		}
		if extension.GetNamespace() == "codegraph.metadata" && json.Unmarshal(extension.GetJson(), &metadata) == nil && metadata.FnRef {
			return true
		}
	}
	return false
}

// v2 positions are zero-based; CodeGraph prints one-based start lines.
func lineSuffix(node *graphv2.Node) string {
	if start := node.GetLocation().GetStart(); start.Line != nil {
		return fmt.Sprintf(":%d", start.GetLine()+1)
	}
	return ""
}

func startLine(node *graphv2.Node) int32 { return node.GetLocation().GetStart().GetLine() + 1 }

// canonicalSections sorts the per-definition sections of a multi-definition
// answer. Upstream orders same-named definitions by FTS5 BM25 score; GraphNest
// orders them by generated flag, path and line (documented difference). The
// title, each section's exact text and the trailing note still must match.
func canonicalSections(text string) string {
	if !strings.Contains(text, "distinct definitions") {
		return text
	}
	body, note, _ := strings.Cut(text, "\n\n> **Note:**")
	parts := strings.Split(body, "\n\n")
	if strings.HasPrefix(parts[0], "**Impact of") {
		// Impact sections are separated by a blank line and end in their own blank line.
		parts = strings.Split(body, "\n\n\n")
	}
	title, sections := parts[0], parts[1:]
	slices.Sort(sections)
	return title + "\n\n" + strings.Join(sections, "\n\n") + "\n\n> **Note:**" + note
}

// impactEntries flattens impact answers to sorted "section|file|name:line"
// entries; sections are labelled by their heading.
func impactEntries(text string) []string {
	var entries []string
	section, file := "", ""
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "**Impact: "):
			section, _, _ = strings.Cut(strings.TrimPrefix(line, "**Impact: "), " affects")
		case strings.HasPrefix(line, "**") && strings.HasSuffix(line, ":**"):
			file = strings.TrimSuffix(strings.TrimPrefix(line, "**"), ":**")
		case line != "" && file != "" && !strings.HasPrefix(line, ">"):
			for _, node := range strings.Split(line, ", ") {
				entries = append(entries, section+"|"+file+"|"+node)
			}
		}
	}
	slices.Sort(entries)
	return entries
}
