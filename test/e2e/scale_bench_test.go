//go:build e2e && unix

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/indexer"
	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/search"
	"github.com/balcsida/graphnest/internal/zoekt"
	"github.com/balcsida/graphnest/pkg/api"
)

// TestScaleBenchmark indexes every <GRAPHNEST_SCALE_REPOS>/<owner>/<name>.git through the real
// indexer, PostgreSQL and Zoekt, then times REST searches and writes one JSON report.
func TestScaleBenchmark(t *testing.T) {
	corpus := os.Getenv("GRAPHNEST_SCALE_REPOS")
	if corpus == "" {
		t.Skip("GRAPHNEST_SCALE_REPOS is not set")
	}
	_, zoektWebserver := requiredExecutables(t)
	zoektIndex := requiredExecutable(t, "ZOEKT_INDEX")
	ctx := t.Context()
	database := newMilestoneDatabase(t)

	gitDirs, err := filepath.Glob(filepath.Join(corpus, "*", "*.git"))
	if err != nil || len(gitDirs) == 0 {
		t.Fatalf("no <owner>/<name>.git directories in %s: %v", corpus, err)
	}
	var fakes []fakeRepository
	var sourceBytes, sourceFiles int64
	sourceByName := map[string]int64{}
	var repositoryIDs []int64
	for index, gitDir := range gitDirs {
		rel, _ := filepath.Rel(corpus, gitDir)
		name := strings.TrimSuffix(filepath.ToSlash(rel), ".git")
		// A repository git cannot read is still registered (with a sha that cannot be archived)
		// so that it is reported under repositories.failed instead of vanishing.
		fake := fakeRepository{id: int64(index + 1), name: name, sha: strings.Repeat("0", 40), defaultBranch: "main"}
		if branch, err := gitOutput(ctx, gitDir, "symbolic-ref", "--short", "HEAD"); err == nil {
			if head, err := gitOutput(ctx, gitDir, "rev-parse", "HEAD"); err == nil {
				fake.defaultBranch, fake.sha = strings.TrimSpace(branch), strings.TrimSpace(head)
				listing, err := gitOutput(ctx, gitDir, "ls-tree", "-r", "-l", "HEAD")
				if err != nil {
					t.Fatalf("ls-tree %s: %v", name, err)
				}
				for _, line := range strings.Split(listing, "\n") {
					meta, _, _ := strings.Cut(line, "\t")
					if fields := strings.Fields(meta); len(fields) == 4 && fields[1] == "blob" {
						size, _ := strconv.ParseInt(fields[3], 10, 64)
						sourceBytes += size
						sourceFiles++
						sourceByName[name] += size
					}
				}
				fake.sizeKB = sourceByName[name] / 1024
			}
		}
		fakes = append(fakes, fake)
		repositoryIDs = append(repositoryIDs, fake.id)
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	github := &fakeGHES{gitRoot: corpus, appID: "7", publicKey: &privateKey.PublicKey, repositories: fakes}
	github.server = httptest.NewTLSServer(http.HandlerFunc(github.serveHTTP))
	t.Cleanup(github.server.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: github.server.Certificate().Raw})
	base, err := url.Parse(github.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	apiBase := *base
	apiBase.Path = "/api/v3"
	endpoints := githubapp.Endpoints{Web: base, API: &apiBase, Upload: base, Git: base, Archive: base}
	httpClient, err := githubapp.NewHTTPClient(caPEM, endpoints, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := githubapp.NewSigner(7, privateKeyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	metrics := observability.New()
	githubClient := githubapp.NewClient(endpoints, httpClient, signer, "2022-11-28", 8<<20, nil, metrics)
	if err := githubapp.NewReconciler(githubClient, database.store).All(ctx); err != nil {
		t.Fatal(err)
	}
	// Archive errors are always retryable with backoff of up to 5 minutes; one attempt makes them final.
	if _, err := database.pool.Exec(ctx, "update index_jobs set max_attempts=1"); err != nil {
		t.Fatal(err)
	}

	indexDir := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	zoektAddress := freeAddress(t)
	zoektProcess := startProcess(t, exec.CommandContext(ctx, zoektWebserver, "-index", indexDir, "-listen", zoektAddress, "-rpc", "-html=false"))
	t.Cleanup(func() { zoektProcess.stop(t) })
	zoektClient, err := zoekt.New("http://"+zoektAddress, &http.Client{Timeout: 10 * time.Second}, 256<<10, metrics)
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, zoektClient, zoektProcess)
	if err := database.store.UpsertSearchNode(ctx, "primary", "http://"+zoektAddress); err != nil {
		t.Fatal(err)
	}

	worker := &indexer.Worker{
		ID: "scale-worker", Queue: database.store, Store: database.store, Tokens: githubClient,
		Snapshots: indexer.ArchiveSnapshotProvider{Client: githubClient, WorkspacesDir: filepath.Join(t.TempDir(), "archives"), Limits: indexer.ArchiveLimits{
			MaxDownloadBytes: 1 << 30, MaxExtractedBytes: 5 << 30, MaxFileBytes: 512 << 20, MaxFiles: 200000, MaxPathBytes: 4096,
		}},
		Zoekt: &indexer.ZoektIndexer{
			Binary: zoektIndex, IndexDir: indexDir, Runner: indexer.Runner{MaxOutput: 64 << 10, KillGrace: 100 * time.Millisecond},
			Client: zoektClient, IndexTimeout: 10 * time.Minute, VisibilityTimeout: 2 * time.Minute,
		},
		RenewEvery: time.Second, CleanupTimeout: 5 * time.Second, MaxRepositoryBytes: 5 << 30, Metrics: metrics,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	type timing struct {
		Name   string `json:"name"`
		Bytes  int64  `json:"source_bytes"`
		Millis int64  `json:"ms"`
	}
	var timings []timing
	indexStart := time.Now()
	for {
		start := time.Now()
		worked, err := worker.RunOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
		var name string
		if err := database.pool.QueryRow(ctx, `select r.owner||'/'||r.name from index_jobs j join repositories r on r.id=j.repository_id
			order by j.updated_at desc limit 1`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		timings = append(timings, timing{name, sourceByName[name], time.Since(start).Milliseconds()})
	}
	indexWall := time.Since(indexStart)

	failed := map[string][]string{}
	rows, err := database.pool.Query(ctx, `select coalesce(j.error_code,''), r.owner||'/'||r.name from index_jobs j
		join repositories r on r.id=j.repository_id where j.state='failed' order by 2`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var code, name string
		if err := rows.Scan(&code, &name); err != nil {
			t.Fatal(err)
		}
		failed[code] = append(failed[code], name)
	}
	rows.Close()
	// The indexer's own phase timers say where the indexing time went.
	scrape := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	phaseSeconds := map[string]float64{}
	for _, sum := range regexp.MustCompile(`(?m)^graphnest_index_phase_duration_seconds_sum\{phase="(\w+)",result="\w+"\} (\S+)$`).FindAllStringSubmatch(scrape.Body.String(), -1) {
		seconds, _ := strconv.ParseFloat(sum[2], 64)
		phaseSeconds[sum[1]] += seconds
	}
	var indexed int
	if err := database.pool.QueryRow(ctx, "select count(*) from repositories where status='ready'").Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	var shardBytes int64
	_ = filepath.WalkDir(indexDir, func(_ string, entry fs.DirEntry, _ error) error {
		if info, err := entry.Info(); err == nil && !entry.IsDir() {
			shardBytes += info.Size()
		}
		return nil
	})
	var databaseBytes int64
	if err := database.pool.QueryRow(ctx, `select coalesce(sum(pg_total_relation_size(c.oid)),0)::bigint from pg_class c
		join pg_namespace n on n.oid=c.relnamespace where n.nspname=current_schema() and c.relkind='r'`).Scan(&databaseBytes); err != nil {
		t.Fatal(err)
	}

	// Administrator without a method searches every active repository.
	authenticator := authn.NewStatic(map[string]authn.Principal{token: {Subject: "bench", Administrator: true}})
	searchService := search.NewService(zoektClient, authz.NewPostgres(database.store), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	mux := http.NewServeMux()
	httpapi.RegisterSearch(mux, authn.RequestAuthenticator{Bearer: authenticator}, searchService, 64<<10, 256<<10)
	server := httptest.NewServer(mux)
	defer server.Close()
	queries := []string{"func main", "TODO", "import", "class", "error", "http", "README", "foo.*bar", "file:README"}
	const requests, concurrency = 200, 4
	// pass sends every request once and summarizes it overall and per query.
	pass := func() map[string]any {
		latencies := make([]float64, requests)
		statuses := make([]int, requests)
		var wait sync.WaitGroup
		jobs := make(chan int)
		for range concurrency {
			wait.Add(1)
			go func() {
				defer wait.Done()
				for i := range jobs {
					body, _ := json.Marshal(api.SearchRequest{Query: queries[i%len(queries)], Limit: 20})
					request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/search", bytes.NewReader(body))
					request.Header.Set("Authorization", "Bearer "+token)
					request.Header.Set("Content-Type", "application/json")
					start := time.Now()
					if response, err := server.Client().Do(request); err == nil {
						_, _ = io.Copy(io.Discard, response.Body)
						response.Body.Close()
						statuses[i] = response.StatusCode
					}
					latencies[i] = float64(time.Since(start).Microseconds()) / 1000
				}
			}()
		}
		for i := range requests {
			jobs <- i
		}
		close(jobs)
		wait.Wait()
		// The latencies include failed requests; errors says how many there were.
		summarize := func(keep func(int) bool) map[string]any {
			var kept []float64
			failed := 0
			for i, latency := range latencies {
				if keep(i) {
					kept = append(kept, latency)
					if statuses[i] != http.StatusOK {
						failed++
					}
				}
			}
			sort.Float64s(kept)
			return map[string]any{"requests": len(kept), "errors": failed, "p50_ms": percentile(kept, 0.5), "p95_ms": percentile(kept, 0.95), "max_ms": percentile(kept, 1)}
		}
		summary := summarize(func(int) bool { return true })
		byQuery := map[string]any{}
		for index, query := range queries {
			byQuery[query] = summarize(func(i int) bool { return i%len(queries) == index })
		}
		summary["by_query"] = byQuery
		return summary
	}
	// The first pass meets shards that were only just written; the second shows what caching changes.
	firstPass, secondPass := pass(), pass()

	millis := make([]float64, len(timings))
	for i, entry := range timings {
		millis[i] = float64(entry.Millis)
	}
	sort.Float64s(millis)
	sort.Slice(timings, func(i, j int) bool { return timings[i].Millis > timings[j].Millis })
	report := map[string]any{
		"date": time.Now().UTC().Format(time.RFC3339), "go_os": runtime.GOOS, "go_arch": runtime.GOARCH, "cpus": runtime.NumCPU(),
		"repositories": map[string]any{"total": len(fakes), "indexed": indexed, "failed": failed},
		"source":       map[string]any{"bytes": sourceBytes, "files": sourceFiles},
		"index": map[string]any{
			"wall_seconds":      indexWall.Seconds(),
			"phase_seconds":     phaseSeconds,
			"per_repository_ms": map[string]float64{"p50": percentile(millis, 0.5), "p95": percentile(millis, 0.95), "max": percentile(millis, 1)},
			"shard_bytes":       shardBytes,
			"slowest":           timings[:min(5, len(timings))],
		},
		"database_bytes": databaseBytes,
		"search":         map[string]any{"concurrency": concurrency, "first_pass": firstPass, "second_pass": secondPass},
		"notes": []string{
			"fetch is a local git archive served by a fake GitHub, not a GitHub download",
			"the indexer runs one job at a time",
			"a single Zoekt process runs on the same host as the indexer, PostgreSQL client and load generator",
			"graph and SCIP queries are not measured",
		},
	}
	output, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scale report:\n%s", output)
	if path := os.Getenv("GRAPHNEST_SCALE_REPORT"); path != "" {
		if err := os.WriteFile(path, append(output, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gitOutput(ctx context.Context, gitDir string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, "git", append([]string{"--git-dir", gitDir}, args...)...).Output()
	return string(output), err
}

// percentile reads a nearest-rank percentile from an ascending slice; empty gives 0.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(q*float64(len(sorted)) + 0.999999)
	return sorted[min(max(rank, 1), len(sorted))-1]
}
