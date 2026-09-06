package graphartifact

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"google.golang.org/protobuf/proto"
)

// Test-only SQLite bridge: Python's stdlib is already required by test/parity.
// Read the committed oracle directly; no new production SQLite/Node dependency.
type fixtureRow map[string]json.RawMessage
type fixtureTables map[string][]fixtureRow

func fixtureRows(t *testing.T) fixtureTables {
	t.Helper()
	data, err := exec.Command("python3", "-c", `import sqlite3,json
c=sqlite3.connect("file:../../test/fixtures/codegraph/reference.db?mode=ro",uri=True)
c.row_factory=sqlite3.Row
print(json.dumps({t:[dict(r) for r in c.execute('SELECT * FROM '+t+' ORDER BY rowid')] for t in ['nodes','edges','files','unresolved_refs','project_metadata']}))`).Output()
	if err != nil {
		t.Fatal(err)
	}
	var rows fixtureTables
	if err = json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func rowValue[T any](t *testing.T, r fixtureRow, key string) *T {
	t.Helper()
	raw, ok := r[key]
	if !ok {
		t.Fatalf("missing oracle column %q", key)
	}
	var value *T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(key, err)
	}
	return value
}
func rowString(t *testing.T, r fixtureRow, key string) string { return *rowValue[string](t, r, key) }
func rowBool(t *testing.T, r fixtureRow, key string) *bool {
	if v := rowValue[int64](t, r, key); v != nil {
		return proto.Bool(*v != 0)
	}
	return nil
}
func rowList(t *testing.T, r fixtureRow, key string) *graphv2.StringList {
	if v := rowValue[string](t, r, key); v != nil {
		var values []string
		if err := json.Unmarshal([]byte(*v), &values); err != nil {
			t.Fatal(err)
		}
		return &graphv2.StringList{Values: values}
	}
	return nil
}
func rowExtension(t *testing.T, r fixtureRow, key, namespace string) *graphv2.Extension {
	if v := rowValue[string](t, r, key); v != nil {
		return &graphv2.Extension{Namespace: namespace, Json: []byte(*v)}
	}
	return nil
}
func fixturePoint(line, col *int32) *graphv2.Position {
	if line == nil && col == nil {
		return nil
	}
	if line != nil {
		line = proto.Int32(*line - 1)
	}
	return &graphv2.Position{Line: line, Character: col}
}
func fixtureLocation(line, col *int32) *graphv2.Location {
	if p := fixturePoint(line, col); p != nil {
		return &graphv2.Location{Start: p}
	}
	return nil
}

func fixtureV2(t *testing.T, rows fixtureTables) *graphv2.Artifact {
	t.Helper()
	a := minimalV2()
	a.Nodes = nil
	for _, r := range rows["nodes"] {
		n := &graphv2.Node{SourceId: rowString(t, r, "id"), Occurrence: rowString(t, r, "id"), Kind: rowString(t, r, "kind"), Name: rowString(t, r, "name"), QualifiedName: rowString(t, r, "qualified_name"), Path: rowValue[string](t, r, "file_path"), Language: rowString(t, r, "language"), Documentation: rowValue[string](t, r, "docstring"), Signature: rowValue[string](t, r, "signature"), Visibility: rowValue[string](t, r, "visibility"), IsExported: rowBool(t, r, "is_exported"), IsAsync: rowBool(t, r, "is_async"), IsStatic: rowBool(t, r, "is_static"), IsAbstract: rowBool(t, r, "is_abstract"), Decorators: rowList(t, r, "decorators"), TypeParameters: rowList(t, r, "type_parameters"), ReturnType: rowValue[string](t, r, "return_type"), UpdatedAt: rowValue[int64](t, r, "updated_at")}
		n.Location = &graphv2.Location{Start: fixturePoint(rowValue[int32](t, r, "start_line"), rowValue[int32](t, r, "start_column")), End: fixturePoint(rowValue[int32](t, r, "end_line"), rowValue[int32](t, r, "end_column"))}
		a.Nodes = append(a.Nodes, n)
	}
	for _, r := range rows["edges"] {
		kind, ok := ParseRelationship(rowString(t, r, "kind"))
		if !ok {
			t.Fatal("unknown edge")
		}
		e := &graphv2.Edge{SourceId: strconv.FormatInt(*rowValue[int64](t, r, "id"), 10), Source: rowString(t, r, "source"), Target: rowString(t, r, "target"), Kind: kind.WireKind(), Location: fixtureLocation(rowValue[int32](t, r, "line"), rowValue[int32](t, r, "col")), Provenance: rowValue[string](t, r, "provenance")}
		if ext := rowExtension(t, r, "metadata", "codegraph.edge-metadata"); ext != nil {
			e.Extensions = []*graphv2.Extension{ext}
			var metadata struct {
				Confidence *float64
				ResolvedBy *string
			}
			if err := json.Unmarshal(ext.Json, &metadata); err != nil {
				t.Fatal(err)
			}
			e.Confidence = metadata.Confidence
			e.ResolutionReason = metadata.ResolvedBy
		}
		// The source row ID is retained separately. Occurrence uses all evidence;
		// the ordinal only distinguishes truly identical repeated occurrences.
		copy := proto.Clone(e).(*graphv2.Edge)
		copy.SourceId = ""
		key, _ := proto.MarshalOptions{Deterministic: true}.Marshal(copy)
		e.Occurrence = fixtureOccurrence("edge", key, a.Edges)
		a.Edges = append(a.Edges, e)
	}
	for _, r := range rows["files"] {
		a.Files = append(a.Files, &graphv2.File{Path: rowString(t, r, "path"), ContentHash: rowString(t, r, "content_hash"), Language: rowString(t, r, "language"), Size: *rowValue[int64](t, r, "size"), ModifiedAt: rowValue[int64](t, r, "modified_at"), IndexedAt: rowValue[int64](t, r, "indexed_at"), NodeCount: rowValue[int64](t, r, "node_count"), Generated: rowBool(t, r, "generated"), Errors: rowExtension(t, r, "errors", "codegraph.extraction-errors")})
	}
	for _, r := range rows["unresolved_refs"] {
		ref := &graphv2.UnresolvedReference{SourceId: strconv.FormatInt(*rowValue[int64](t, r, "id"), 10), Source: rowString(t, r, "from_node_id"), Name: rowString(t, r, "reference_name"), Kind: rowString(t, r, "reference_kind"), Location: fixtureLocation(rowValue[int32](t, r, "line"), rowValue[int32](t, r, "col")), Candidates: rowList(t, r, "candidates"), Path: rowValue[string](t, r, "file_path"), Language: rowValue[string](t, r, "language"), Status: rowValue[string](t, r, "status"), NameTail: rowValue[string](t, r, "name_tail")}
		copy := proto.Clone(ref).(*graphv2.UnresolvedReference)
		copy.SourceId = ""
		key, _ := proto.MarshalOptions{Deterministic: true}.Marshal(copy)
		ref.Occurrence = fixtureOccurrence("ref", key, a.Unresolved)
		a.Unresolved = append(a.Unresolved, ref)
	}
	for _, r := range rows["project_metadata"] {
		a.Metadata = append(a.Metadata, &graphv2.MetadataEntry{Key: rowString(t, r, "key"), Value: rowString(t, r, "value"), UpdatedAt: rowValue[int64](t, r, "updated_at")})
	}
	return a
}

func fixtureOccurrence[T interface{ GetOccurrence() string }](prefix string, key []byte, previous []T) string {
	id, _ := IdentityV2(&graphv2.Producer{Name: "codegraph", Version: "1.6.0"}, "fixture", prefix, string(key))
	ordinal := 0
	for _, p := range previous {
		if strings.HasPrefix(p.GetOccurrence(), id+":") {
			ordinal++
		}
	}
	return fmt.Sprintf("%s:%d", id, ordinal)
}

func fixtureBool(v *bool) *int {
	if v == nil {
		return nil
	}
	i := 0
	if *v {
		i = 1
	}
	return &i
}
func fixtureJSONList(v *graphv2.StringList) *string {
	if v == nil {
		return nil
	}
	values := v.Values
	if values == nil {
		values = []string{}
	}
	b, _ := json.Marshal(values)
	return proto.String(string(b))
}
func fixtureJSONExtension(v *graphv2.Extension) *string {
	if v == nil {
		return nil
	}
	return proto.String(string(v.Json))
}
func fixtureLine(p *graphv2.Position) *int32 {
	if p == nil || p.Line == nil {
		return nil
	}
	return proto.Int32(*p.Line + 1)
}

func fixtureBack(a *graphv2.Artifact) map[string][]map[string]any {
	rows := map[string][]map[string]any{}
	for _, n := range a.Nodes {
		rows["nodes"] = append(rows["nodes"], map[string]any{"id": n.SourceId, "kind": n.Kind, "name": n.Name, "qualified_name": n.QualifiedName, "file_path": n.Path, "language": n.Language, "start_line": fixtureLine(n.Location.Start), "end_line": fixtureLine(n.Location.End), "start_column": n.Location.Start.Character, "end_column": n.Location.End.Character, "docstring": n.Documentation, "signature": n.Signature, "visibility": n.Visibility, "is_exported": fixtureBool(n.IsExported), "is_async": fixtureBool(n.IsAsync), "is_static": fixtureBool(n.IsStatic), "is_abstract": fixtureBool(n.IsAbstract), "decorators": fixtureJSONList(n.Decorators), "type_parameters": fixtureJSONList(n.TypeParameters), "return_type": n.ReturnType, "updated_at": n.UpdatedAt})
	}
	for _, e := range a.Edges {
		kind, _ := RelationshipFromWire(e.Kind)
		id, _ := strconv.ParseInt(e.SourceId, 10, 64)
		var metadata *string
		if len(e.Extensions) > 0 {
			metadata = fixtureJSONExtension(e.Extensions[0])
		}
		p := e.GetLocation().GetStart()
		var col *int32
		if p != nil {
			col = p.Character
		}
		rows["edges"] = append(rows["edges"], map[string]any{"id": id, "source": e.Source, "target": e.Target, "kind": kind.Name, "metadata": metadata, "line": fixtureLine(p), "col": col, "provenance": e.Provenance})
	}
	for _, f := range a.Files {
		rows["files"] = append(rows["files"], map[string]any{"path": f.Path, "content_hash": f.ContentHash, "language": f.Language, "size": f.Size, "modified_at": f.ModifiedAt, "indexed_at": f.IndexedAt, "node_count": f.NodeCount, "generated": fixtureBool(f.Generated), "errors": fixtureJSONExtension(f.Errors)})
	}
	for _, r := range a.Unresolved {
		id, _ := strconv.ParseInt(r.SourceId, 10, 64)
		p := r.Location.Start
		rows["unresolved_refs"] = append(rows["unresolved_refs"], map[string]any{"id": id, "from_node_id": r.Source, "reference_name": r.Name, "reference_kind": r.Kind, "line": fixtureLine(p), "col": p.Character, "candidates": fixtureJSONList(r.Candidates), "file_path": r.Path, "language": r.Language, "status": r.Status, "name_tail": r.NameTail})
	}
	for _, m := range a.Metadata {
		rows["project_metadata"] = append(rows["project_metadata"], map[string]any{"key": m.Key, "value": m.Value, "updated_at": m.UpdatedAt})
	}
	return rows
}

func TestV2CodeGraphSQLiteLossless(t *testing.T) {
	want := fixtureRows(t)
	a := fixtureV2(t, want)
	data, err := MarshalV2(a, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseV2(data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(a, got) {
		t.Fatal("v2 wire lost fixture evidence")
	}
	back := fixtureBack(got)
	for table, rows := range want {
		if len(rows) != len(back[table]) {
			t.Fatal(table, "row count")
		}
		for i, row := range rows {
			before, _ := json.Marshal(row)
			after, _ := json.Marshal(back[table][i])
			var x, y any
			json.Unmarshal(before, &x)
			json.Unmarshal(after, &y)
			if !reflect.DeepEqual(x, y) {
				t.Fatalf("%s row %d lost fields\nwant %s\ngot  %s", table, i, before, after)
			}
		}
	}
	// Oracle's real CRLF/astral/accented prefix: byte and UTF-16 columns differ.
	source, err := os.ReadFile("../../test/fixtures/codegraph/source/unicode.ts")
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	for _, e := range got.Edges {
		if e.Kind != graphv2.EdgeKind_EDGE_KIND_CALLS {
			continue
		}
		var node *graphv2.Node
		for _, n := range got.Nodes {
			if n.Occurrence == e.Source {
				node = n
			}
		}
		if node == nil || node.GetPath() != "unicode.ts" {
			continue
		}
		position := e.GetLocation().GetStart()
		offset, err := SourceOffset(string(source), position)
		if err != nil || !strings.HasPrefix(string(source[offset:]), "normalize(") {
			t.Fatalf("actual producer call coordinate: %v %v", position, err)
		}
		lineStart := strings.LastIndexByte(string(source[:offset]), '\n') + 1
		if offset-lineStart == int(position.GetCharacter()) {
			t.Fatal("fixture did not distinguish UTF-8 bytes from UTF-16")
		}
		checked = true
	}
	if !checked || !strings.Contains(string(source), "\r\n") {
		t.Fatal("missing unicode CRLF coordinate coverage")
	}
}

func TestV2SyntheticVocabulary(t *testing.T) {
	data, err := os.ReadFile("../../test/fixtures/codegraph/synthetic-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Nodes []struct{ ID, Kind string }
		Edges []struct{ Source, Target, Kind string }
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	a := minimalV2()
	a.Nodes = nil
	for _, n := range fixture.Nodes {
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: n.ID, Occurrence: n.ID, Kind: n.Kind})
	}
	for _, e := range fixture.Edges {
		r, ok := ParseRelationship(e.Kind)
		if !ok {
			t.Fatal(e.Kind)
		}
		a.Edges = append(a.Edges, &graphv2.Edge{Occurrence: e.Kind, Source: e.Source, Target: e.Target, Kind: r.WireKind()})
	}
	data, err = MarshalV2(a, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseV2(data, Limits{})
	if err != nil || !proto.Equal(a, got) {
		t.Fatal("synthetic vocabulary", err)
	}
	if len(got.Nodes) != 23 || len(got.Edges) != 13 {
		t.Fatal("fixture vocabulary changed")
	}
}
