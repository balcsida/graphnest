//go:build integration

package postgres

import (
	"fmt"
	"os"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"google.golang.org/protobuf/proto"
)

// TestGraphV2IndexPlanEvidence is an opt-in diagnostic, not an optimizer-plan
// assertion or a production latency benchmark. Its default skip is not evidence.
func TestGraphV2IndexPlanEvidence(t *testing.T) {
	if os.Getenv("GRAPHNEST_GRAPH_INDEX_EVIDENCE") != "1" {
		t.Skip("diagnostic not run; set GRAPHNEST_GRAPH_INDEX_EVIDENCE=1 with the required PostgreSQL test DSN")
	}
	s, id := readyGraphStore(t, testSHA('a'))
	var version, seqscan string
	if err := s.pool.QueryRow(t.Context(), `show server_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(t.Context(), `show enable_seqscan`).Scan(&seqscan); err != nil {
		t.Fatal(err)
	}
	if seqscan != "on" {
		t.Fatalf("normal-planner evidence requires enable_seqscan=on; got %q", seqscan)
	}
	t.Logf("PostgreSQL %s; enable_seqscan=%s; corpus: 5000 function nodes with unique names/paths, 5000 calls in a directed ring", version, seqscan)
	a := &graphv2.Artifact{SchemaVersion: 2, Repository: "101", Commit: testSHA('a'), Producer: &graphv2.Producer{Name: "codegraph", Version: "index-evidence"}}
	for i := 0; i < 5000; i++ {
		key := fmt.Sprint(i)
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: key, Occurrence: key, Kind: "function", Name: "node-" + key, Path: proto.String("file-" + key + ".ts")})
		a.Edges = append(a.Edges, &graphv2.Edge{Occurrence: key, Source: key, Target: fmt.Sprint((i + 1) % 5000), Kind: graphv2.EdgeKind_EDGE_KIND_CALLS})
	}
	result, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "index-evidence"}, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `analyze graph_v2_nodes; analyze graph_v2_edges`); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ sql, parameter, want string }{
		{`select occurrence from graph_v2_nodes where upload_id=$1 and sha256(name)=sha256($2::bytea) and name=$2::bytea`, "node-2345", "2345"},
		{`select occurrence from graph_v2_nodes where upload_id=$1 and sha256(path)=sha256($2::bytea) and path=$2::bytea`, "file-2345.ts", "2345"},
		{`select target from graph_v2_edges where upload_id=$1 and source_key=sha256($2::bytea) and source=$2::bytea and kind=4`, "2345", "2346"},
		{`select source from graph_v2_edges where upload_id=$1 and target_key=sha256($2::bytea) and target=$2::bytea and kind=4`, "2345", "2344"},
	} {
		rows, err := s.pool.Query(t.Context(), check.sql, result.Upload.ID, []byte(check.parameter))
		if err != nil {
			t.Fatal(err)
		}
		var facts []string
		for rows.Next() {
			var fact []byte
			if err := rows.Scan(&fact); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			facts = append(facts, string(fact))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if len(facts) != 1 || facts[0] != check.want {
			t.Fatalf("query=%s upload_id=%d facts=%v want=[%s]", check.sql, result.Upload.ID, facts, check.want)
		}
		statement := "explain (analyze,buffers,settings) " + check.sql
		t.Logf("SQL: %s; parameter $1=%d; parameter $2 bytea hex=%x; checked facts=%v", statement, result.Upload.ID, []byte(check.parameter), facts)
		rows, err = s.pool.Query(t.Context(), statement, result.Upload.ID, []byte(check.parameter))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			t.Log(line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
}
