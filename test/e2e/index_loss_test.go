//go:build e2e && unix

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/admin"
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

func TestLostIndexVolumeRecovers(t *testing.T) {
	_, zoektWebserver := requiredExecutables(t)
	zoektIndex := requiredExecutable(t, "ZOEKT_INDEX")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	database := newMilestoneDatabase(t)
	root := t.TempDir()

	primary := newSmartGitOrigin(t, ctx, root, "acme/source", []string{"package fixture\nconst Needle = \"" + milestoneNeedle + "\"\n"})
	empty := newSmartGitOrigin(t, ctx, root, "acme/empty", nil)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	github := newFakeGHES(t, root, primary, empty, 7, &privateKey.PublicKey)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: github.server.Certificate().Raw})
	base, err := url.Parse(github.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	apiBase := *base
	apiBase.Path = "/api/v3"
	endpoints := githubapp.Endpoints{Web: base, API: &apiBase, Upload: base, Git: base, Archive: base}
	httpClient, err := githubapp.NewHTTPClient(caPEM, endpoints, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := githubapp.NewSigner(7, privateKeyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	metrics := observability.New()
	githubClient := githubapp.NewClient(endpoints, httpClient, signer, "2022-11-28", 2<<20, nil, metrics)

	indexDir := filepath.Join(root, "index")
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	zoektAddress := freeAddress(t)
	var zoektProcess *managedProcess
	startZoekt := func() {
		zoektProcess = startProcess(t, exec.CommandContext(ctx, zoektWebserver, "-index", indexDir, "-listen", zoektAddress, "-rpc", "-html=false"))
	}
	startZoekt()
	t.Cleanup(func() { zoektProcess.stop(t) })
	zoektClient, err := zoekt.New("http://"+zoektAddress, &http.Client{Timeout: 3 * time.Second}, 256<<10, metrics)
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, zoektClient, zoektProcess)
	if err := database.store.UpsertSearchNode(ctx, "primary", "http://"+zoektAddress); err != nil {
		t.Fatal(err)
	}

	// A durable administrator (no installation, no repository scope) may search and reindex any repository.
	authenticator := authn.NewStatic(map[string]authn.Principal{token: {Subject: "operator", Administrator: true}})
	requestAuth := authn.RequestAuthenticator{Bearer: authenticator}
	searchService := search.NewService(zoektClient, authz.NewPostgres(database.store), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	mux := http.NewServeMux()
	httpapi.RegisterSearch(mux, requestAuth, searchService, 64<<10, 256<<10)
	httpapi.RegisterAdmin(mux, requestAuth, &admin.Service{Store: database.store, Audit: database.store, GitHub: githubClient}, 100, 64<<10, 256<<10)
	server := httptest.NewServer(mux)
	defer server.Close()

	worker := &indexer.Worker{
		ID: "e2e-worker", Queue: database.store, Store: database.store, Tokens: githubClient,
		Snapshots: indexer.ArchiveSnapshotProvider{Client: githubClient, WorkspacesDir: filepath.Join(root, "data", "archives"), Limits: indexer.ArchiveLimits{
			MaxDownloadBytes: 2 << 20, MaxExtractedBytes: 4 << 20, MaxFileBytes: 1 << 20, MaxFiles: 100, MaxPathBytes: 1024,
		}},
		Zoekt: &indexer.ZoektIndexer{
			Binary: zoektIndex, IndexDir: indexDir, Runner: indexer.Runner{MaxOutput: 64 << 10, KillGrace: 100 * time.Millisecond},
			Client: zoektClient, IndexTimeout: 15 * time.Second, VisibilityTimeout: 10 * time.Second,
		},
		RenewEvery: time.Second, CleanupTimeout: 5 * time.Second,
	}
	sha := primary.shas[0]
	runWorker := func() {
		t.Helper()
		if worked, err := worker.RunOne(ctx); err != nil || !worked {
			t.Fatalf("index worker: worked=%v err=%v", worked, err)
		}
	}
	assertFound := func() {
		t.Helper()
		matches := restSearch(t, server, api.SearchRequest{Query: milestoneNeedle}).Matches
		if len(matches) != 1 || matches[0].SHA != sha || matches[0].Repository.ID != milestoneRepositoryID {
			t.Fatalf("search matches = %#v, want one at %s", matches, sha)
		}
	}

	reconciler := githubapp.NewReconciler(githubClient, database.store)
	if err := reconciler.Installation(ctx, milestoneInstallationID); err != nil {
		t.Fatal(err)
	}
	assertQueuedTarget(t, database.pool, milestoneRepositoryID, sha, 1)
	runWorker()
	zoektID := authorizedRepository(t, database.store, milestoneRepositoryID).ZoektID
	assertVisible(t, ctx, zoektClient, zoektID, "main", sha)
	assertPublished(t, database.pool, milestoneRepositoryID, sha)
	assertFound()

	zoektProcess.stop(t)
	entries, err := os.ReadDir(indexDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("index directory entries=%v err=%v, want shards before deletion", entries, err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(indexDir, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	startZoekt()
	waitReady(t, ctx, zoektClient, zoektProcess)

	// Nothing notices the missing shard on its own: search is silently empty while PostgreSQL still says ready.
	if matches := restSearch(t, server, api.SearchRequest{Query: milestoneNeedle}).Matches; len(matches) != 0 {
		t.Fatalf("search after index loss = %#v, want none", matches)
	}
	if listed, err := zoektClient.List(ctx, zoektID); err != nil || len(listed) != 0 {
		t.Fatalf("Zoekt list after index loss = %#v err=%v, want empty", listed, err)
	}
	assertPublished(t, database.pool, milestoneRepositoryID, sha)

	// The statement the operations guide gives for this case; the next reconcile queues what it marks.
	if _, err := database.pool.Exec(ctx, "update repositories set status = 'pending' where enabled and not archived"); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Installation(ctx, milestoneInstallationID); err != nil {
		t.Fatal(err)
	}
	assertQueuedTarget(t, database.pool, milestoneRepositoryID, sha, 1)
	runWorker()
	assertVisible(t, ctx, zoektClient, zoektID, "main", sha)
	assertPublished(t, database.pool, milestoneRepositoryID, sha)
	assertFound()

	// An administrator can queue one repository through the API, also when nothing looks wrong with it.
	if status := restJSON(t, server, http.MethodPost, "/v1/admin/repositories/101/reindex", nil, nil); status != http.StatusNoContent {
		t.Fatalf("admin reindex status = %d, want 204", status)
	}
	assertQueuedTarget(t, database.pool, milestoneRepositoryID, sha, 1)
	runWorker()
	assertPublished(t, database.pool, milestoneRepositoryID, sha)
	assertFound()
}
