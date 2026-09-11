//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/graphquery"
)

func TestGraphGenerationRemainsReadableAfterReplacement(t *testing.T) {
	s, repositoryID := readyGraphStore(t, testSHA('a'))
	first, err := s.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifactFor(repositoryID, testSHA('a'), "old"))
	if err != nil {
		t.Fatal(err)
	}
	snapshots := []graphquery.QuerySnapshot{{RepositoryID: repositoryID, UploadID: first.Upload.ID, Commit: testSHA('a')}}
	next := artifactFor(repositoryID, testSHA('a'), "new")
	next.Nodes[1].QualifiedName = "New"
	if _, err := s.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, next); err != nil {
		t.Fatal(err)
	}
	old, err := s.LoadGraph(t.Context(), first.Upload.ID)
	if err != nil || old.Analyzer.Name != "old" {
		t.Fatalf("retired generation lost: analyzer=%q err=%v", old.Analyzer.Name, err)
	}
	symbols, err := s.Symbols(t.Context(), graphquery.SymbolQuery{Snapshots: snapshots, Limit: 10})
	if err != nil || len(symbols) != 1 || symbols[0].Name != "Thing" {
		t.Fatalf("pinned symbols=%#v err=%v", symbols, err)
	}
	if _, err := s.pool.Exec(t.Context(), `update repositories set indexed_sha=$2 where id=$1`, repositoryID, testSHA('b')); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifactFor(repositoryID, testSHA('b'), "later")); err != nil {
		t.Fatal(err)
	}
	symbols, err = s.Symbols(t.Context(), graphquery.SymbolQuery{Snapshots: snapshots, Limit: 10})
	if err != nil || len(symbols) != 1 || symbols[0].Name != "Thing" {
		t.Fatalf("SHA advance lost pinned symbols=%#v err=%v", symbols, err)
	}
	manifests, err := s.GraphManifests(t.Context())
	if err != nil || len(manifests) != 1 || manifests[0].Commit != testSHA('b') {
		t.Fatalf("manifests=%#v err=%v", manifests, err)
	}
}

func TestGraphV2StorageSchema(t *testing.T) {
	s := migratedStore(t)
	for _, table := range []string{"graph_v2_nodes", "graph_v2_edges", "graph_v2_files", "graph_v2_unresolved", "graph_v2_diagnostics"} {
		var exists bool
		if err := s.pool.QueryRow(t.Context(), `select to_regclass($1) is not null`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("missing %s: %v", table, err)
		}
	}
}

func TestGraphV2RoundTripAndPublicationPreconditions(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	v1, err := s.ReplaceGraph(t.Context(), id, GraphSourceManaged, artifactFor(id, testSHA('a'), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	artifact := storageV2Artifact()
	options := GraphPublication{Publisher: "user:42", Capabilities: []string{"calls", "metadata"}, ExpectedActiveID: v1.Upload.ID}
	if _, err := s.ReplaceGraphV2(t.Context(), id, options, artifact); !errors.Is(err, ErrGraphProviderConflict) {
		t.Fatalf("provider change=%v", err)
	}
	options.AllowProviderChange = true
	result, err := s.ReplaceGraphV2(t.Context(), id, options, artifact)
	if err != nil || !result.Applied {
		t.Fatalf("publish=%#v err=%v", result, err)
	}
	loaded, err := s.LoadGraphV2(t.Context(), id, result.Upload.ID)
	if err != nil || !proto.Equal(artifact, loaded) {
		t.Fatalf("roundtrip mismatch: %v, got %v", err, loaded)
	}
	var absentVisibility, emptyVisibility int
	if err := s.pool.QueryRow(t.Context(), `select count(*) filter(where visibility is null),count(*) filter(where visibility=''::bytea) from graph_v2_nodes where upload_id=$1`, result.Upload.ID).Scan(&absentVisibility, &emptyVisibility); err != nil || absentVisibility != 1 || emptyVisibility != 1 {
		t.Fatalf("visibility projection lost empty/absent: absent=%d empty=%d err=%v", absentVisibility, emptyVisibility, err)
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, options, artifact); !errors.Is(err, ErrGraphPrecondition) {
		t.Fatalf("stale precondition=%v", err)
	}
	if _, err := s.LoadGraph(t.Context(), result.Upload.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("v1 reader accepted v2: %v", err)
	}
	manifests, err := s.GraphManifests(t.Context())
	if err != nil || len(manifests) != 0 {
		t.Fatalf("legacy manifest exposed v2=%v %v", manifests, err)
	}
	legacy, err := s.ReplaceGraph(t.Context(), id, GraphSourceExternal, artifactFor(id, testSHA('a'), "legacy"))
	if err != nil || legacy.Applied {
		t.Fatalf("v1 replaced v2: %v %v", legacy, err)
	}
	var publisher string
	var caps []string
	if err := s.pool.QueryRow(t.Context(), `select publisher,capabilities from graph_uploads where id=$1`, result.Upload.ID).Scan(&publisher, &caps); err != nil || publisher != "user:42" || !reflect.DeepEqual(caps, options.Capabilities) {
		t.Fatalf("identity=%q capabilities=%v err=%v", publisher, caps, err)
	}
	options.ExpectedActiveID = result.Upload.ID
	invalid := proto.Clone(artifact).(*graphv2.Artifact)
	invalid.Edges[0].Target = "missing"
	invalid.ContentHash = nil
	if _, err := s.ReplaceGraphV2(t.Context(), id, options, invalid); !errors.Is(err, graphartifact.ErrInvalidArtifact) {
		t.Fatalf("invalid=%v", err)
	}
	wrongRepository := proto.Clone(artifact).(*graphv2.Artifact)
	wrongRepository.Repository = "999"
	wrongRepository.ContentHash = nil
	if _, err := s.ReplaceGraphV2(t.Context(), id, options, wrongRepository); !errors.Is(err, graphartifact.ErrInvalidArtifact) {
		t.Fatalf("identity mismatch=%v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ReplaceGraphV2(canceled, id, options, artifact); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled=%v", err)
	}
	if _, err := s.pool.Exec(t.Context(), `update repositories set indexed_sha=$2 where id=$1`, id, testSHA('b')); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, options, artifact); !errors.Is(err, ErrGraphPrecondition) {
		t.Fatalf("stale SHA=%v", err)
	}
	loaded, err = s.LoadGraphV2(t.Context(), id, result.Upload.ID)
	if err != nil || !proto.Equal(artifact, loaded) {
		t.Fatalf("previous usable state lost: %v", err)
	}
}

func storageV2Artifact() *graphv2.Artifact {
	a := &graphv2.Artifact{SchemaVersion: 2, Repository: "101", Commit: testSHA('a'), Producer: &graphv2.Producer{Name: "codegraph", Version: "pinned", Configuration: "portable"},
		Nodes: []*graphv2.Node{
			{SourceId: "1", Occurrence: "a", Kind: "function", Name: "A", QualifiedName: "a.A", Path: proto.String("a.ts"), Language: "typescript", IsExported: proto.Bool(false), Visibility: proto.String(""), Decorators: &graphv2.StringList{}},
			{SourceId: "2", Occurrence: "b", Kind: "class", Name: "B", QualifiedName: "b.B", Documentation: proto.String(""), Location: &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(1)}}}},
		Edges:       []*graphv2.Edge{{SourceId: "1", Occurrence: "call-1", Source: "a", Target: "b", Kind: graphv2.EdgeKind_EDGE_KIND_CALLS}, {SourceId: "2", Occurrence: "call-2", Source: "a", Target: "b", Kind: graphv2.EdgeKind_EDGE_KIND_CALLS, Confidence: proto.Float64(0)}},
		Files:       []*graphv2.File{{Path: "a.ts", ContentHash: strings.Repeat("a", 64), Language: "typescript", Size: 0, Generated: proto.Bool(false), Errors: &graphv2.Extension{Namespace: "codegraph.errors", Json: []byte(`[]`)}}},
		Unresolved:  []*graphv2.UnresolvedReference{{SourceId: "3", Occurrence: "unresolved", Source: "a", Name: "unknown", Kind: "function_ref", Candidates: &graphv2.StringList{}}},
		Diagnostics: []*graphv2.Diagnostic{{Occurrence: "diagnostic", Message: "partial", Severity: "warning"}},
		Metadata:    []*graphv2.MetadataEntry{{Key: "project", Value: "test", UpdatedAt: proto.Int64(1)}}, ImportedAt: 10,
		Extensions: []*graphv2.Extension{{Namespace: "codegraph.test", Json: []byte(`{"large":9007199254740993}`)}}}
	hash, err := graphartifact.SemanticHashV2(a, graphartifact.Limits{})
	if err != nil {
		panic(err)
	}
	a.ContentHash = hash
	return a
}

func TestGraphV2CopyFailurePreservesGeneration(t *testing.T) {
	for _, mode := range []string{"constraint", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			s, id := readyGraphStore(t, testSHA('a'))
			old, err := s.ReplaceGraph(t.Context(), id, GraphSourceManaged, artifactFor(id, testSHA('a'), "old"))
			if err != nil {
				t.Fatal(err)
			}
			options := GraphPublication{Publisher: "user:42", ExpectedActiveID: old.Upload.ID, AllowProviderChange: true}
			ctx := t.Context()
			if mode == "constraint" {
				if _, err := s.pool.Exec(ctx, `alter table graph_v2_edges add check (occurrence <> 'call-2')`); err != nil {
					t.Fatal(err)
				}
			} else {
				barrier, err := s.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer barrier.Rollback(t.Context())
				if _, err := barrier.Exec(ctx, `lock table graph_v2_edges in access exclusive mode`); err != nil {
					t.Fatal(err)
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			_, err = s.ReplaceGraphV2(ctx, id, options, storageV2Artifact())
			if err == nil {
				t.Fatal("copy unexpectedly succeeded")
			}
			if mode == "cancellation" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancellation=%v", err)
			}
			var active int64
			var count int
			if err := s.pool.QueryRow(t.Context(), `select id from graph_uploads where repository_id=$1 and active`, id).Scan(&active); err != nil || active != old.Upload.ID {
				t.Fatalf("previous active=%d err=%v", active, err)
			}
			if err := s.pool.QueryRow(t.Context(), `select count(*) from graph_uploads where repository_id=$1`, id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("leaked uploads=%d err=%v", count, err)
			}
		})
	}
}

func TestGraphV2ConcurrentPublishComparesGeneration(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "user:42"}, storageV2Artifact())
			results <- err
		}()
	}
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrGraphPrecondition) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestGraphConcurrentV1Precedence(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	results := make(chan error, 2)
	for _, source := range []GraphSource{GraphSourceManaged, GraphSourceExternal} {
		go func() {
			_, err := s.ReplaceGraph(t.Context(), id, source, artifactFor(id, testSHA('a'), string(source)))
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	manifests, err := s.GraphManifests(t.Context())
	if err != nil || len(manifests) != 1 || manifests[0].Source != "external" {
		t.Fatalf("manifests=%v err=%v", manifests, err)
	}
}

func TestLoadGraphUsesOneGenerationSnapshot(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	original := artifactFor(id, testSHA('a'), "old")
	upload, err := s.ReplaceGraph(t.Context(), id, GraphSourceManaged, original)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(t.Context())
	if _, err := barrier.Exec(t.Context(), `lock table graph_nodes in access exclusive mode`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		artifact graphartifact.Artifact
		err      error
	}
	done := make(chan result, 1)
	go func() { a, err := s.LoadGraph(t.Context(), upload.Upload.ID); done <- result{a, err} }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := s.pool.QueryRow(t.Context(), `select exists(select 1 from pg_locks where relation='graph_nodes'::regclass and not granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reader did not reach node query")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := barrier.Exec(t.Context(), `delete from graph_uploads where id=$1`, upload.Upload.ID); err != nil {
		t.Fatal(err)
	}
	if err := barrier.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || !graphArtifactsEqual(got.artifact, original) {
		t.Fatalf("mixed snapshot=%#v err=%v", got.artifact, got.err)
	}
}

func TestGraphV2SchemaRejectsCrossGenerationFacts(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	first, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "test"}, storageV2Artifact())
	if err != nil {
		t.Fatal(err)
	}
	next := storageV2Artifact()
	next.Nodes[1].Occurrence = "other"
	for _, e := range next.Edges {
		e.Target = "other"
	}
	next.ContentHash = nil
	second, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "test", ExpectedActiveID: first.Upload.ID}, next)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`update graph_v2_edges set target='b' where upload_id=$1`,
		`update graph_v2_edges set kind=14 where upload_id=$1`,
		`update graph_v2_edges set confidence='NaN' where upload_id=$1`,
		`update graph_v2_nodes set kind='unknown' where upload_id=$1`,
		`update graph_v2_files set content_hash='bad' where upload_id=$1`,
		`insert into graph_nodes(upload_id,uid,kind,path,language,symbol_kind,qualified_name,signature,scip_symbol,start_line,start_character,end_line,end_character) values($1,'v1',3,'','','','','','',0,0,0,0)`,
	} {
		if _, err := s.pool.Exec(t.Context(), query, second.Upload.ID); err == nil {
			t.Fatalf("invalid fact accepted: %s", query)
		}
	}
	loaded, err := s.LoadGraphV2(t.Context(), id, first.Upload.ID)
	if err != nil || !proto.Equal(loaded, storageV2Artifact()) {
		t.Fatalf("retired v2 read=%v err=%v", loaded, err)
	}
	if _, err := s.LoadGraphV2(t.Context(), id+1, first.Upload.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross repository read=%v", err)
	}
	// Execute the documented maintenance SQL against this drained test schema.
	tx, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err := tx.Exec(t.Context(), `lock table graph_uploads in access exclusive mode; delete from graph_uploads where not active`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadGraphV2(t.Context(), id, first.Upload.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cleanup kept retired generation: %v", err)
	}
	if _, err := s.LoadGraphV2(t.Context(), id, second.Upload.ID); err != nil {
		t.Fatalf("cleanup removed active generation: %v", err)
	}
}

func TestMigratePreservesPopulatedV1Generation(t *testing.T) {
	pool := testPool(t)
	if err := migrateThrough(t.Context(), pool, 26); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	id := seedReadyRepository(t, s, 101, testSHA('a'))
	original := artifactFor(id, testSHA('a'), "legacy")
	original.Nodes[1].QualifiedName = incompressibleGraphString("v1-name", graphartifact.DefaultMaxIdentifierBytes)
	original.Nodes[1].Path = incompressibleGraphString("v1-path", graphartifact.DefaultMaxPathBytes)
	if err := graphartifact.Validate(original, graphartifact.Limits{}); err != nil {
		t.Fatalf("invalid legacy fixture: %v", err)
	}
	var uploadID int64
	if err := pool.QueryRow(t.Context(), `insert into graph_uploads(repository_id,commit,schema_version,source,analyzer_name,analyzer_version,content_hash,node_count,edge_count) values($1,$2,1,'managed','legacy','1',$3,2,1) returning id`, id, original.Commit, original.ContentHash).Scan(&uploadID); err != nil {
		t.Fatal(err)
	}
	for _, n := range original.Nodes {
		if _, err := pool.Exec(t.Context(), `insert into graph_nodes(upload_id,uid,kind,path,language,symbol_kind,qualified_name,signature,scip_symbol,start_line,start_character,end_line,end_character) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, uploadID, n.UID, int16(n.Kind), n.Path, n.Language, n.SymbolKind, n.QualifiedName, n.Signature, n.SCIPSymbol, n.Range.StartLine, n.Range.StartCharacter, n.Range.EndLine, n.Range.EndCharacter); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(t.Context(), `insert into graph_edges(upload_id,source_uid,target_uid,kind,path,start_line,start_character,end_line,end_character,confidence,resolution_reason) values($1,'repository','symbol',1,'a.go',0,0,0,0,1,'')`, uploadID); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadGraph(t.Context(), uploadID)
	if err != nil || !graphArtifactsEqual(loaded, original) {
		t.Fatalf("migration changed v1 data: %#v %v", loaded, err)
	}
	if _, err := s.ReplaceGraph(t.Context(), id, GraphSourceExternal, artifactFor(id, testSHA('a'), "new")); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.LoadGraph(t.Context(), uploadID)
	if err != nil || !graphArtifactsEqual(loaded, original) {
		t.Fatalf("post-upgrade replacement lost v1 data: %#v %v", loaded, err)
	}
}

func TestMigrateLegacyBackfillFailureIsRetryable(t *testing.T) {
	pool := testPool(t)
	if err := migrateThrough(t.Context(), pool, 18); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	id := seedReadyRepository(t, s, 101, testSHA('a'))
	seedLegacySCIP(t, s, id, uploadWith("legacy.go", globalSymbol, definitionRole))
	if _, err := pool.Exec(t.Context(), `create function reject_graph_backfill() returns trigger language plpgsql as $$begin raise exception 'forced backfill failure'; end$$;
 create trigger reject_graph_backfill before insert on graph_nodes for each statement execute function reject_graph_backfill()`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), pool); err == nil {
		t.Fatal("backfill failure accepted")
	}
	var applied int
	var activeColumn bool
	if err := pool.QueryRow(t.Context(), `select count(*) from schema_migrations where version>=19`).Scan(&applied); err != nil || applied != 0 {
		t.Fatalf("failed backfill recorded migrations=%d err=%v", applied, err)
	}
	if err := pool.QueryRow(t.Context(), `select exists(select 1 from information_schema.columns where table_schema=current_schema() and table_name='graph_uploads' and column_name='active')`).Scan(&activeColumn); err != nil || activeColumn {
		t.Fatalf("failed upgrade retained DDL=%v err=%v", activeColumn, err)
	}
	if _, err := pool.Exec(t.Context(), `drop trigger reject_graph_backfill on graph_nodes; drop function reject_graph_backfill()`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	manifests, err := s.GraphManifests(t.Context())
	if err != nil || len(manifests) != 1 || manifests[0].Source != "scip" {
		t.Fatalf("retry skipped backfill=%v err=%v", manifests, err)
	}
}

func TestGraphV2StoresAllRegisteredKinds(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	kinds := []string{"repository", "symbol", "file", "module", "class", "struct", "interface", "trait", "protocol", "function", "method", "property", "field", "variable", "constant", "enum", "enum_member", "type_alias", "namespace", "parameter", "import", "export", "route", "component", "union"}
	for _, kind := range kinds {
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: kind, Occurrence: kind, Kind: kind, Name: kind})
	}
	for kind := 1; kind <= 13; kind++ {
		a.Edges = append(a.Edges, &graphv2.Edge{Occurrence: fmt.Sprintf("relation-%d", kind), Source: "a", Target: "b", Kind: graphv2.EdgeKind(kind)})
	}
	result, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "test"}, a)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadGraphV2(t.Context(), id, result.Upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ContentHash) != 0 {
		t.Fatal("storage mutated caller hash")
	}
	a.ContentHash = loaded.ContentHash
	if len(loaded.ContentHash) != 32 || !proto.Equal(a, loaded) {
		t.Fatal("kind roundtrip or calculated hash differs")
	}
	var nodeKinds, edgeKinds int
	if err := s.pool.QueryRow(t.Context(), `select (select count(distinct kind) from graph_v2_nodes where upload_id=$1),(select count(distinct kind) from graph_v2_edges where upload_id=$1)`, result.Upload.ID).Scan(&nodeKinds, &edgeKinds); err != nil || nodeKinds != 25 || edgeKinds != 13 {
		t.Fatalf("node kinds=%d edge kinds=%d err=%v", nodeKinds, edgeKinds, err)
	}
	var absent, zero int
	if err := s.pool.QueryRow(t.Context(), `select count(*) filter(where confidence is null),count(*) filter(where confidence=0) from graph_v2_edges where upload_id=$1`, result.Upload.ID).Scan(&absent, &zero); err != nil || absent != 14 || zero != 1 {
		t.Fatalf("confidence presence lost: absent=%d zero=%d err=%v", absent, zero, err)
	}
}

// Hex digests are deterministic, incompressible enough to expose PostgreSQL's
// B-tree tuple ceiling; repeating one character would conceal the failure.
func incompressibleGraphString(seed string, length int) string {
	var out strings.Builder
	for i := 0; out.Len() < length; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", seed, i)))
		fmt.Fprintf(&out, "%x", sum)
	}
	return out.String()[:length]
}

func TestGraphV2PreservesContractStringValues(t *testing.T) {
	for _, test := range []string{"max_length", "nul_producer", "nul_node_identity", "nul_node_metadata", "nul_evidence_identity"} {
		t.Run(test, func(t *testing.T) {
			s, id := readyGraphStore(t, testSHA('a'))
			a := storageV2Artifact()
			a.ContentHash = nil
			switch test {
			case "max_length":
				max := graphartifact.DefaultMaxIdentifierBytes
				a.Producer = &graphv2.Producer{Name: incompressibleGraphString("producer-name", max), Version: incompressibleGraphString("producer-version", max), Configuration: incompressibleGraphString("producer-config", max)}
				for i, n := range a.Nodes {
					seed := fmt.Sprint(i)
					n.SourceId = incompressibleGraphString("source-"+seed, max)
					n.Occurrence = incompressibleGraphString("occurrence-"+seed, max)
					n.Name = incompressibleGraphString("name-"+seed, max)
					n.QualifiedName = incompressibleGraphString("qualified-"+seed, max)
					n.Language = incompressibleGraphString("language-"+seed, max)
					n.Visibility = proto.String(incompressibleGraphString("visibility-"+seed, max))
					n.Location = nil
					n.Path = proto.String(incompressibleGraphString("path-"+seed, graphartifact.DefaultMaxPathBytes))
				}
				for i, e := range a.Edges {
					e.SourceId = incompressibleGraphString(fmt.Sprint("edge-source", i), max)
					e.Occurrence = incompressibleGraphString(fmt.Sprint("edge", i), max)
				}
				a.Files[0].Path = *a.Nodes[0].Path
				a.Files[0].Language = incompressibleGraphString("file-language", max)
				a.Unresolved[0].SourceId = incompressibleGraphString("ref-source", max)
				a.Unresolved[0].Occurrence = incompressibleGraphString("ref", max)
				a.Unresolved[0].Name = incompressibleGraphString("ref-name", max)
				a.Unresolved[0].Path = a.Nodes[0].Path
				a.Diagnostics[0].Occurrence = incompressibleGraphString("diagnostic", max)
			case "nul_producer":
				a.Producer = &graphv2.Producer{Name: "code\x00graph", Version: "version\x00one", Configuration: "config\x00portable"}
			case "nul_node_identity":
				a.Nodes[0].SourceId = "source\x00one"
				a.Nodes[0].Occurrence = "node\x00one"
			case "nul_node_metadata":
				a.Nodes[0].Name = "A\x00B"
				a.Nodes[0].QualifiedName = "a.\x00B"
				a.Nodes[0].Language = "type\x00script"
				a.Nodes[0].Visibility = proto.String("pub\x00lic")
				a.Files[0].Language = "type\x00script"
			case "nul_evidence_identity":
				a.Edges[0].SourceId = "edge-source\x00one"
				a.Edges[0].Occurrence = "edge\x00one"
				a.Unresolved[0].SourceId = "ref-source\x00one"
				a.Unresolved[0].Occurrence = "ref\x00one"
				a.Unresolved[0].Name = "unknown\x00name"
				a.Diagnostics[0].Occurrence = "diagnostic\x00one"
				a.Metadata[0].Key = "meta\x00key"
				a.Metadata[0].Value = "meta\x00value"
			}
			for _, e := range a.Edges {
				e.Source = a.Nodes[0].Occurrence
				e.Target = a.Nodes[1].Occurrence
			}
			a.Unresolved[0].Source = a.Nodes[0].Occurrence
			if err := graphartifact.ValidateV2(a, graphartifact.Limits{}); err != nil {
				t.Fatalf("invalid accepted-string fixture: %v", err)
			}
			hash, err := graphartifact.SemanticHashV2(a, graphartifact.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			a.ContentHash = hash
			publication := GraphPublication{Publisher: "test"}
			result, err := s.ReplaceGraphV2(t.Context(), id, publication, a)
			if err != nil {
				t.Fatalf("valid %s publication failed: %v", test, err)
			}
			loaded, err := s.LoadGraphV2(t.Context(), id, result.Upload.ID)
			if err != nil || !proto.Equal(a, loaded) {
				t.Fatalf("valid %s roundtrip changed: %v", test, err)
			}
			var name, qualified, path, language, visibility []byte
			if err := s.pool.QueryRow(t.Context(), `select name,qualified_name,path,language,visibility from graph_v2_nodes where upload_id=$1 and occurrence_key=sha256($2::bytea) and occurrence=$2::bytea`, result.Upload.ID, []byte(a.Nodes[0].Occurrence)).Scan(&name, &qualified, &path, &language, &visibility); err != nil {
				t.Fatal(err)
			}
			n := a.Nodes[0]
			if string(name) != n.Name || string(qualified) != n.QualifiedName || string(language) != n.Language || (path == nil) != (n.Path == nil) || (visibility == nil) != (n.Visibility == nil) || n.Path != nil && string(path) != *n.Path || n.Visibility != nil && string(visibility) != *n.Visibility {
				t.Fatal("query projections changed original bytes or optional presence")
			}
			var producerName, producerVersion, producerConfig []byte
			if err := s.pool.QueryRow(t.Context(), `select producer_name,producer_version,producer_configuration from graph_uploads where id=$1`, result.Upload.ID).Scan(&producerName, &producerVersion, &producerConfig); err != nil {
				t.Fatal(err)
			}
			if string(producerName) != a.Producer.Name || string(producerVersion) != a.Producer.Version || string(producerConfig) != a.Producer.Configuration {
				t.Fatal("producer projections changed original bytes")
			}
			// Exact same producer bytes remain the same provider, including embedded NUL.
			publication.ExpectedActiveID = result.Upload.ID
			next, err := s.ReplaceGraphV2(t.Context(), id, publication, a)
			if err != nil {
				t.Fatalf("same provider bytes were not recognized: %v", err)
			}
			if test == "nul_producer" {
				changed := proto.Clone(a).(*graphv2.Artifact)
				changed.ContentHash = nil
				changed.Producer.Name = "code\x00different"
				publication.ExpectedActiveID = next.Upload.ID
				if _, err := s.ReplaceGraphV2(t.Context(), id, publication, changed); !errors.Is(err, ErrGraphProviderConflict) {
					t.Fatalf("distinct NUL-containing provider aliased original: %v", err)
				}
			}
		})
	}
}
