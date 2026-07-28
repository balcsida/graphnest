//go:build integration && unix

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphcommand"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEmbeddedAndStandaloneCommandParity(t *testing.T) {
	dsn, pool, store := parityStore(t)
	artifact := parityArtifact(t, pool, store)
	secret := []byte("graph-command-parity-secret")
	embeddedAddress, standaloneAddress := freeAddress(t), freeAddress(t)
	root := t.TempDir()

	indexerSettings := parityIndexerSettings(t, dsn, root, embeddedAddress, secret)
	embedded, err := newIndexRuntime(t.Context(), indexerSettings)
	if err != nil {
		t.Fatal(err)
	}
	embeddedCtx, stopEmbedded := context.WithCancel(t.Context())
	embeddedDone := make(chan error, 1)
	go func() { embeddedDone <- embedded.run(embeddedCtx) }()
	t.Cleanup(func() {
		stopEmbedded()
		if err := <-embeddedDone; err != nil {
			t.Error(err)
		}
	})

	standaloneCtx, stopStandalone := context.WithCancel(t.Context())
	standaloneDone := make(chan error, 1)
	go func() {
		standaloneDone <- graphcommand.RunStandalone(standaloneCtx, config.Graph{
			Mode: "separate", DatabaseURL: dsn, ListenAddress: standaloneAddress,
			DataDir: filepath.Join(root, "standalone"), InternalSecret: secret,
			SyncInterval: 20 * time.Millisecond, QueryTimeout: 5 * time.Second,
			InterruptGrace: 2 * time.Second, ReadConnections: 2,
			QueryLimits: indexerSettings.Graph.QueryLimits,
		}, nil)
	}()
	t.Cleanup(func() {
		stopStandalone()
		if err := <-standaloneDone; err != nil {
			t.Error(err)
		}
	})

	scope := fmt.Sprintf(`{"repositories":[{"id":101,"name":"acme/repo","commit":%q}]}`, artifact.Commit)
	requests := map[string]string{
		"/internal/v1/graph/context": `{"scope":` + scope + `,"uid":"A"}`,
		"/internal/v1/graph/impact":  `{"scope":` + scope + `,"target_uid":"B"}`,
		"/internal/v1/graph/trace":   `{"scope":` + scope + `,"source_uid":"A","target_uid":"B"}`,
		"/internal/v1/graph/cypher":  `{"admin":true,"statement":"RETURN 1 AS value"}`,
	}
	for _, path := range []string{
		"/internal/v1/graph/context", "/internal/v1/graph/impact",
		"/internal/v1/graph/trace", "/internal/v1/graph/cypher",
	} {
		embeddedResponse := parityRequest(t, embeddedAddress, path, requests[path], secret)
		standaloneResponse := parityRequest(t, standaloneAddress, path, requests[path], secret)
		if !strings.HasPrefix(embeddedResponse, "200 application/json ") {
			t.Fatalf("%s unexpected response: %s", path, embeddedResponse)
		}
		if embeddedResponse != standaloneResponse {
			t.Fatalf("%s differs:\nembedded: %s\nstandalone: %s",
				path, embeddedResponse, standaloneResponse)
		}
	}
}

func parityStore(t *testing.T) (string, *pgxpool.Pool, *postgres.Store) {
	t.Helper()
	dsn := os.Getenv("GREPNEST_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GREPNEST_TEST_POSTGRES_DSN is not set")
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "graph_parity_" + hex.EncodeToString(random)
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "create schema "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "drop schema "+identifier+" cascade")
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	pool, err := postgres.Open(t.Context(), parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return parsed.String(), pool, postgres.New(pool)
}

func parityArtifact(t *testing.T, pool *pgxpool.Pool, store *postgres.Store) graphartifact.Artifact {
	t.Helper()
	if err := store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{
		GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{
		GitHubID: 101, InstallationID: 10, Owner: "acme", Name: "repo",
		CloneURL: "https://example.invalid/repo.git", WebURL: "https://example.invalid/repo",
		DefaultBranch: "main", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	if _, err := pool.Exec(t.Context(), `update repositories set indexed_sha=$2, status='ready' where id=$1`, repository.ID, commit); err != nil {
		t.Fatal(err)
	}
	artifact := graphartifact.Artifact{
		SchemaVersion: 1, RepositoryID: repository.ID, Commit: commit,
		ContentHash: bytes.Repeat([]byte{1}, 32),
		Analyzer:    graphartifact.Analyzer{Name: "parity", Version: "1"},
		Nodes: []graphartifact.Node{
			{UID: "repository", Kind: graphartifact.NodeRepository},
			{UID: "file", Kind: graphartifact.NodeFile, Path: "a.go"},
			{UID: "A", Kind: graphartifact.NodeSymbol, Path: "a.go", Language: "go", QualifiedName: "A"},
			{UID: "B", Kind: graphartifact.NodeSymbol, Path: "a.go", Language: "go", QualifiedName: "B"},
		},
		Edges: []graphartifact.Edge{
			{SourceUID: "repository", TargetUID: "file", Kind: graphartifact.EdgeContains, Path: "a.go", Confidence: 1},
			{SourceUID: "file", TargetUID: "A", Kind: graphartifact.EdgeContains, Path: "a.go", Confidence: 1},
			{SourceUID: "file", TargetUID: "B", Kind: graphartifact.EdgeContains, Path: "a.go", Confidence: 1},
			{SourceUID: "A", TargetUID: "B", Kind: graphartifact.EdgeCalls, Path: "a.go", Confidence: 1},
		},
	}
	if _, err := store.ReplaceGraph(t.Context(), repository.ID, postgres.GraphSourceManaged, artifact); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func parityIndexerSettings(t *testing.T, dsn, root, graphAddress string, secret []byte) config.Indexer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(root, "github.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir, indexDir := filepath.Join(root, "embedded"), filepath.Join(root, "index")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return config.Indexer{
		DatabaseURL: dsn, ZoektURL: "http://127.0.0.1:1",
		MetricsListenAddress: freeAddress(t), DataDir: dataDir, IndexDir: indexDir,
		GitPath: "/usr/bin/git", ZoektGitIndex: "/usr/bin/true", WorkerID: "parity",
		MinFreeBytes: 1, MaxRepositoryBytes: 1 << 20,
		GitHub: config.GitHub{
			AppID: 1, PrivateKeyFile: keyFile, APIVersion: "2022-11-28",
			WebURL: "https://example.invalid", APIURL: "https://example.invalid",
			UploadURL: "https://example.invalid", GitURL: "https://example.invalid",
		},
		Graph: config.Graph{
			Mode: "embedded", ListenAddress: graphAddress, DataDir: dataDir,
			InternalSecret: secret, SyncInterval: 20 * time.Millisecond,
			QueryTimeout: 5 * time.Second, InterruptGrace: 2 * time.Second, ReadConnections: 2,
			QueryLimits: config.GraphQueryLimits{
				PerCategory: 10, DefaultImpactDepth: 3, MaxDepth: 5,
				DefaultTraceDepth: 3, MaxTraceDepth: 5, MaxRows: 100,
				MaxNodes: 100, MaxEdges: 100, MaxFanout: 10,
			},
		},
	}
}

func parityRequest(t *testing.T, address, path, body string, secret []byte) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			"http://"+address+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+string(secret))
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, data); err != nil {
				t.Fatalf("%s: invalid JSON %q", path, data)
			}
			return fmt.Sprintf("%d %s %s", response.StatusCode,
				response.Header.Get("Content-Type"), compact.String())
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not start: %v", address, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
