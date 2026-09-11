//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type graphBenchmarkStatement struct {
	SQL  string `json:"sql"`
	Args []any  `json:"arguments"`
}

// This tracer counts actual pgx statements, including transaction control.
// Capturing arguments is enabled only outside the timed measurements.
type graphBenchmarkTracer struct {
	sync.Mutex
	capture    bool
	reads      int
	statements int
	queries    []graphBenchmarkStatement
}

func (tracer *graphBenchmarkTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tracer.Lock()
	defer tracer.Unlock()
	tracer.statements++
	sql := strings.ToLower(strings.TrimSpace(data.SQL))
	if strings.HasPrefix(sql, "select ") || strings.HasPrefix(sql, "with ") {
		tracer.reads++
		if tracer.capture {
			tracer.queries = append(tracer.queries, graphBenchmarkStatement{SQL: data.SQL, Args: slices.Clone(data.Args)})
		}
	}
	return ctx
}

func (*graphBenchmarkTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tracer *graphBenchmarkTracer) reset(capture bool) {
	tracer.Lock()
	defer tracer.Unlock()
	tracer.capture, tracer.reads, tracer.statements, tracer.queries = capture, 0, 0, nil
}

// TestGraphQueryPostgresBaseline is an opt-in measurement, not a conformance
// gate. A skipped run is not baseline evidence. Set GRAPHNEST_POSTGRES_BASELINE
// to the report path and GRAPHNEST_REQUIRE_POSTGRES=1 to record measurements.
func TestGraphQueryPostgresBaseline(t *testing.T) {
	output := os.Getenv("GRAPHNEST_POSTGRES_BASELINE")
	if output == "" {
		t.Skip("measurement only: set GRAPHNEST_POSTGRES_BASELINE to record a report")
	}
	if os.Getenv("GRAPHNEST_REQUIRE_POSTGRES") != "1" {
		t.Fatal("baseline recording requires GRAPHNEST_REQUIRE_POSTGRES=1")
	}
	const symbolCount, symbolsPerFile, repetitions, samples, warmup = 50_000, 100, 5, 200, 20
	ctx := t.Context()
	commit := testSHA('a')
	store, repositoryID := readyGraphStore(t, commit)
	var schema string
	if err := store.pool.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	cleanupPool := store.pool
	t.Cleanup(func() {
		// testing cancels t.Context before cleanup; retain an uncanceled deadline.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := cleanupPool.Exec(cleanupCtx, "drop schema "+pgx.Identifier{schema}.Sanitize()+" cascade"); err != nil {
			t.Errorf("remove measurement schema: %v", err)
		}
	})
	artifact := graphBenchmarkCorpus(repositoryID, commit, symbolCount, symbolsPerFile)
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := store.ReplaceGraph(ctx, repositoryID, GraphSourceManaged, artifact)
	if err != nil || !replacement.Applied {
		t.Fatalf("seed corpus: replacement=%+v error=%v", replacement, err)
	}
	// Analyze only this test's isolated schema; never change another corpus.
	for _, table := range []string{"installations", "repositories", "graph_uploads", "graph_nodes", "graph_edges"} {
		if _, err := store.pool.Exec(ctx, "analyze "+pgx.Identifier{table}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	tracer := &graphBenchmarkTracer{}
	config := store.pool.Config()
	config.MaxConns, config.MinConns = 1, 0
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	store = New(pool)
	service := &graphquery.Service{Store: store}
	scope := graphprotocol.Scope{SelectedRepositoryID: repositoryID, Repositories: []graphprotocol.RepositorySnapshot{{ID: repositoryID, Name: "acme/benchmark", Commit: commit}}}
	contextRequest := graphprotocol.ContextRequest{Scope: scope, UID: graphBenchmarkUID(1000), Relations: []string{"calls"}, PerCategoryLimit: 10}
	impactRequest := graphprotocol.ImpactRequest{Scope: scope, TargetUID: graphBenchmarkUID(0), Direction: "downstream", Relations: []string{"calls"}, MaxDepth: 3, IncludeTests: true}
	traceRequest := graphprotocol.TraceRequest{Scope: scope, SourceUID: graphBenchmarkUID(0), TargetUID: graphBenchmarkUID(62), MaxDepth: 3}
	tasks := []struct {
		name    string
		request any
		run     func() (any, error)
		want    [][]int
	}{
		{"context", contextRequest, func() (any, error) { return service.Context(ctx, contextRequest) }, [][]int{{969, 993, 999}, {1001, 1007, 1031}}},
		{"impact", impactRequest, func() (any, error) { return service.Impact(ctx, impactRequest) }, [][]int{{1, 7, 31}, {2, 8, 14, 32, 38, 62}, {3, 9, 15, 21, 33, 39, 45, 63, 69, 93}}},
		{"trace", traceRequest, func() (any, error) { return service.Trace(ctx, traceRequest) }, [][]int{{0, 31, 62}}},
	}
	results := map[string]any{}
	for _, task := range tasks {
		tracer.reset(true)
		value, err := task.run()
		if err != nil {
			t.Fatal(err)
		}
		tracer.Lock()
		statements, reads := tracer.statements, tracer.reads
		captured := slices.Clone(tracer.queries)
		tracer.capture = false
		tracer.Unlock()
		graphBenchmarkCheck(t, task.name, value, task.want, commit)
		response, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		runs, p50s, p95s := []any{}, []int64{}, []int64{}
		for run := range repetitions {
			for range warmup {
				if _, err := task.run(); err != nil {
					t.Fatal(err)
				}
			}
			tracer.reset(false)
			durations := make([]int64, samples)
			for i := range durations {
				start := time.Now()
				if _, err := task.run(); err != nil {
					t.Fatal(err)
				}
				durations[i] = time.Since(start).Nanoseconds()
			}
			tracer.Lock()
			gotStatements, gotReads := tracer.statements, tracer.reads
			tracer.Unlock()
			if gotStatements != statements*samples || gotReads != reads*samples {
				t.Fatalf("%s SQL count changed: statements=%d reads=%d want=%d/%d", task.name, gotStatements, gotReads, statements*samples, reads*samples)
			}
			slices.Sort(durations)
			p50, p95 := durations[(samples*50+99)/100-1], durations[(samples*95+99)/100-1]
			p50s, p95s = append(p50s, p50), append(p95s, p95)
			runs = append(runs, map[string]any{"run": run + 1, "samples": samples, "p50_ns": p50, "p95_ns": p95, "min_ns": durations[0], "max_ns": durations[len(durations)-1], "sql_statements": gotStatements, "sql_reads": gotReads})
		}
		slices.Sort(p50s)
		slices.Sort(p95s)
		plans := graphBenchmarkPlans(t, pool, captured)
		results[task.name] = map[string]any{"request": task.request, "expected_symbol_numbers": task.want, "answer_validation": "passed before timing", "response_json_bytes": len(response), "sql_statements_per_operation": statements, "sql_reads_per_operation": reads, "transaction_statements_per_operation": statements - reads, "runs": runs, "median_p50_ns": p50s[repetitions/2], "median_p95_ns": p95s[repetitions/2], "p95_budget_ns": float64(p95s[repetitions/2]) * 1.10, "plans": plans}
		t.Logf("%s p50=%s p95=%s statements=%d reads=%d", task.name, time.Duration(p50s[repetitions/2]), time.Duration(p95s[repetitions/2]), statements, reads)
	}
	var database json.RawMessage
	if err := pool.QueryRow(ctx, `select json_build_object('version',version(),'database',current_database(),'schema',current_schema(),
		'database_bytes',pg_database_size(current_database()),'settings',(select json_object_agg(name,setting) from pg_settings where name in
		('shared_buffers','effective_cache_size','work_mem','plan_cache_mode','random_page_cost','seq_page_cost','track_io_timing','max_connections','server_encoding')),
		'schema_relations',(select json_agg(json_build_object('table',relname,'heap_bytes',pg_relation_size(relid),'index_bytes',pg_indexes_size(relid),'total_bytes',pg_total_relation_size(relid),'rows',n_live_tup)) from pg_stat_user_tables where schemaname=current_schema()))`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	commandOutput := func(command string, args ...string) string {
		data, err := exec.Command(command, args...).Output()
		if err != nil {
			return "unavailable"
		}
		return strings.TrimSpace(string(data))
	}
	file, err := os.ReadFile("graph_query_benchmark_test.go")
	if err != nil {
		t.Fatal(err)
	}
	harnessHash := sha256.Sum256(file)
	report := map[string]any{
		"schema_version": 1, "recorded_at": time.Now().UTC().Format(time.RFC3339),
		"scope":       "Existing graphquery service using the production PostgreSQL Store over TCP; single client, committed snapshot, warm queries. Synthetic managed v1 corpus; not CodeGraph output.",
		"unmeasured":  []string{"REST/MCP transport and graphservice authorization", "browser latency", "CodeGraph production or ingestion", "multi-repository authorization/selectivity", "cold cache", "concurrent throughput", "production repository distributions", "peak process RSS"},
		"environment": map[string]any{"graphnest_commit": commandOutput("git", "rev-parse", "HEAD"), "go": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH, "gomaxprocs": runtime.GOMAXPROCS(0), "os": commandOutput("uname", "-srm"), "cpu": commandOutput("sysctl", "-n", "machdep.cpu.brand_string"), "host_memory_bytes": commandOutput("sysctl", "-n", "hw.memsize"), "database": database, "pool_max_connections": 1, "statement_cache_capacity": config.ConnConfig.StatementCacheCapacity, "default_query_exec_mode": config.ConnConfig.DefaultQueryExecMode.String(), "isolation": "Shared developer host and task-owned PostgreSQL container; isolated per-test schema, no CPU isolation."},
		"corpus":      map[string]any{"generator": "graphBenchmarkCorpus", "producer": artifact.Analyzer, "artifact_schema": 1, "artifact_sha256": hex.EncodeToString(artifact.ContentHash), "artifact_json_bytes": len(artifactJSON), "hash_definition": "SHA256 of encoding/json Artifact before ContentHash is assigned", "repositories": 1, "symbols": symbolCount, "files": symbolCount / symbolsPerFile, "nodes": len(artifact.Nodes), "edges": len(artifact.Edges), "call_edges": symbolCount * 3, "call_offsets_modulo_symbol_count": []int{1, 7, 31}, "call_fanout": 3, "commit": commit, "upload_id": replacement.Upload.ID, "statistics": "ANALYZE ran after loading every fixture table."},
		"method":      map[string]any{"test": "TestGraphQueryPostgresBaseline", "measurement_only": true, "measurement_failures": 0, "command": "GOTOOLCHAIN=go1.26.6 GOWORK=off GOMAXPROCS=1 GRAPHNEST_REQUIRE_POSTGRES=1 GRAPHNEST_POSTGRES_BASELINE=$PWD/docs/parity/codegraph-postgres-baseline.json go test -tags=integration ./internal/postgres -run ^TestGraphQueryPostgresBaseline$ -count=1 -v", "database_connection": "Set GRAPHNEST_TEST_POSTGRES_DSN to the task-owned PostgreSQL instance; credentials are not recorded.", "schema_cleanup": "Only the generated measurement schema is removed using an uncanceled deadline; cleanup errors fail the test.", "opt_in_env": "GRAPHNEST_POSTGRES_BASELINE", "require_postgres": true, "harness_sha256": hex.EncodeToString(harnessHash[:]), "runs": repetitions, "warmup_per_run": warmup, "samples_per_run": samples, "percentiles": "Nearest rank per run, then median of five run percentiles; timer and query tracer overhead included.", "query_counts": "pgx TraceQueryStart counts real SQL statements, with SELECT/WITH reads separated from BEGIN/COMMIT.", "explain": "Executed SQL and arguments captured from answer validation. After warm measurements, EXPLAIN ANALYZE BUFFERS FORMAT JSON EXECUTE replays the actual pgx prepared statement and its planner-selected generic/custom plan. No forced planner settings."},
		"results":     results,
		"budget":      map[string]any{"max_warm_p95_regression_fraction": 0.10, "comparison": "Compare median run p95 against this baseline using the same corpus, host, PostgreSQL and harness settings. The design budget remains exactly 10%; no arbitrary latency threshold is enforced by this measurement-only test."},
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func graphBenchmarkUID(number int) string { return fmt.Sprintf("symbol:%05d", number) }

func graphBenchmarkPlans(t *testing.T, pool *pgxpool.Pool, queries []graphBenchmarkStatement) []any {
	t.Helper()
	ctx := t.Context()
	plans := make([]any, 0, len(queries))
	for _, query := range queries {
		var name string
		var types []string
		var generic, custom int64
		if err := pool.QueryRow(ctx, `select name, parameter_types::text[], generic_plans, custom_plans
			from pg_prepared_statements where statement=$1`, query.SQL).Scan(&name, &types, &generic, &custom); err != nil {
			t.Fatal(err)
		}
		if len(types) != len(query.Args) {
			t.Fatalf("prepared query has %d types for %d arguments", len(types), len(query.Args))
		}
		// PostgreSQL quotes its own typed values; no hand-built SQL literals.
		literals, columns := make([]string, len(types)), make([]string, len(types))
		destinations := make([]any, len(types))
		for i, datatype := range types {
			columns[i] = fmt.Sprintf("quote_nullable($%d::%s)", i+1, datatype)
			destinations[i] = &literals[i]
		}
		if len(types) > 0 {
			if err := pool.QueryRow(ctx, "select "+strings.Join(columns, ","), query.Args...).Scan(destinations...); err != nil {
				t.Fatal(err)
			}
		}
		explain := "explain (analyze, buffers, format json) execute " + pgx.Identifier{name}.Sanitize()
		if len(literals) > 0 {
			explain += "(" + strings.Join(literals, ",") + ")"
		}
		var plan json.RawMessage
		if err := pool.QueryRow(ctx, explain).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal(plan, &decoded); err != nil {
			t.Fatal(err)
		}
		indexes := []string{}
		var visit func(any)
		visit = func(node any) {
			switch node := node.(type) {
			case []any:
				for _, child := range node {
					visit(child)
				}
			case map[string]any:
				if index, ok := node["Index Name"].(string); ok {
					indexes = append(indexes, index)
				}
				for _, child := range node {
					visit(child)
				}
			}
		}
		visit(decoded)
		slices.Sort(indexes)
		indexes = slices.Compact(indexes)
		for _, table := range []string{"graph_nodes", "graph_edges"} {
			if strings.Contains(query.SQL, table) && !slices.ContainsFunc(indexes, func(index string) bool { return strings.HasPrefix(index, table+"_") }) {
				t.Fatalf("query has no %s index access: %s", table, plan)
			}
		}
		plans = append(plans, map[string]any{"query": query, "prepared_statement": name, "generic_plans_before_explain": generic, "custom_plans_before_explain": custom, "indexes_used": indexes, "explain_analyze_buffers": plan})
	}
	return plans
}

func graphBenchmarkCorpus(repositoryID int64, commit string, count, perFile int) graphartifact.Artifact {
	artifact := graphartifact.Artifact{SchemaVersion: 1, RepositoryID: repositoryID, Commit: commit, Analyzer: graphartifact.Analyzer{Name: "synthetic-postgres-baseline", Version: "1"}}
	artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: "repository", Kind: graphartifact.NodeRepository})
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("src/package%03d/file.go", i/perFile)
		fileUID := "file:" + path
		if i%perFile == 0 {
			artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: fileUID, Kind: graphartifact.NodeFile, Path: path, Language: "go"})
			artifact.Edges = append(artifact.Edges, graphartifact.Edge{SourceUID: "repository", TargetUID: fileUID, Kind: graphartifact.EdgeContains, Path: path, Confidence: 1})
		}
		uid := graphBenchmarkUID(i)
		range_ := graphartifact.Range{StartLine: int32(i % perFile * 2), EndLine: int32(i % perFile * 2), EndCharacter: 1}
		artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: uid, Kind: graphartifact.NodeSymbol, Path: path, Language: "go", SymbolKind: "Function", QualifiedName: uid, Range: range_})
		artifact.Edges = append(artifact.Edges, graphartifact.Edge{SourceUID: fileUID, TargetUID: uid, Kind: graphartifact.EdgeContains, Path: path, Range: range_, Confidence: 1})
		for _, offset := range []int{1, 7, 31} {
			artifact.Edges = append(artifact.Edges, graphartifact.Edge{SourceUID: uid, TargetUID: graphBenchmarkUID((i + offset) % count), Kind: graphartifact.EdgeCalls, Path: path, Range: range_, Confidence: 1, ResolutionReason: "synthetic-fixed-offset"})
		}
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		panic(err) // Artifact contains only deterministic JSON-compatible fields.
	}
	hash := sha256.Sum256(data)
	artifact.ContentHash = hash[:]
	return artifact
}

func graphBenchmarkCheck(t *testing.T, name string, value any, want [][]int, commit string) {
	t.Helper()
	var groups [][]graphprotocol.Symbol
	var status string
	wantStatus := graphprotocol.StatusFound
	var boundaries []graphprotocol.Boundary
	var commits map[string]string
	switch response := value.(type) {
	case graphprotocol.ContextResponse:
		status, boundaries, commits = response.Status, response.Boundaries, response.Commits
		if response.Symbol == nil || response.Symbol.UID != graphBenchmarkUID(1000) {
			t.Fatalf("%s wrong context target: %+v", name, response.Symbol)
		}
		groups = [][]graphprotocol.Symbol{response.Incoming["calls"], response.Outgoing["calls"]}
	case graphprotocol.ImpactResponse:
		status, boundaries, commits = response.Status, response.Boundaries, response.Commits
		if len(response.ByDepth) != len(want) {
			t.Fatalf("%s unexpected depths: %+v", name, response.ByDepth)
		}
		for depth := 1; depth <= len(want); depth++ {
			groups = append(groups, response.ByDepth[depth])
		}
	case graphprotocol.TraceResponse:
		wantStatus = graphprotocol.StatusOK
		status, boundaries, commits = response.Status, response.Boundaries, response.Commits
		groups = [][]graphprotocol.Symbol{response.Nodes}
	default:
		t.Fatalf("%s unexpected response type %T", name, value)
	}
	if status != wantStatus || len(boundaries) != 0 || len(commits) != 1 || commits["acme/benchmark"] != commit {
		t.Fatalf("%s incomplete or wrong snapshot: status=%s boundaries=%+v commits=%v", name, status, boundaries, commits)
	}
	for i, group := range groups {
		if len(group) != len(want[i]) {
			t.Fatalf("%s group %d has %d nodes, want %d", name, i, len(group), len(want[i]))
		}
		for j, symbol := range group {
			if symbol.UID != graphBenchmarkUID(want[i][j]) {
				t.Fatalf("%s group %d position %d got %s want %s", name, i, j, symbol.UID, graphBenchmarkUID(want[i][j]))
			}
		}
	}
}
