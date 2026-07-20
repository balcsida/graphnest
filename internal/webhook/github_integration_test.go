//go:build integration

package webhook

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	webhookSHAA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	webhookSHAB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestGitHubProcessorDurability(t *testing.T) {
	t.Run("states and unsupported payload", func(t *testing.T) {
		store, pool := webhookStore(t)
		seedWebhookRepository(t, store, 101)
		processor := NewGitHubProcessor(store, nil)
		deliveries := []Delivery{
			{ID: "accepted", Event: "push", Body: pushBody(10, 101, "refs/heads/main", webhookSHAA)},
			{ID: "ignored", Event: "ping", Body: []byte(`{"zen":"keep it logically awesome","hook":{"id":7},"sender":{"login":"octocat"}}`)},
		}
		for _, delivery := range deliveries {
			if inserted, err := processor.Process(t.Context(), delivery); err != nil || !inserted {
				t.Fatalf("%s: inserted=%v err=%v", delivery.ID, inserted, err)
			}
		}
		rows, err := pool.Query(t.Context(), "select delivery_id, state from webhook_deliveries order by delivery_id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var id, state string
			if err := rows.Scan(&id, &state); err != nil {
				t.Fatal(err)
			}
			got[id] = state
		}
		if err := rows.Err(); err != nil || got["accepted"] != "accepted" || got["ignored"] != "ignored" {
			t.Fatalf("states=%v err=%v", got, err)
		}
	})

	t.Run("push size validation", func(t *testing.T) {
		store, pool := webhookStore(t)
		seedWebhookRepository(t, store, 101)
		processor := NewGitHubProcessor(store, nil)
		for index, body := range [][]byte{
			[]byte(fmt.Sprintf(`{"installation":{"id":10},"repository":{"id":101},"ref":"refs/heads/main","after":%q}`, webhookSHAA)),
			pushBodyWithSize(10, 101, "refs/heads/main", webhookSHAA, -1),
			pushBodyWithSize(10, 101, "refs/heads/main", webhookSHAA, math.MaxInt64/1024+1),
		} {
			_, err := processor.Process(t.Context(), Delivery{ID: fmt.Sprintf("invalid-size-%d", index), Event: "push", Body: body})
			var invalid InvalidDeliveryError
			if !errors.As(err, &invalid) {
				t.Fatalf("case %d: err=%v", index, err)
			}
		}
		if got := deliveryCount(t, pool); got != 3 {
			t.Fatalf("invalid sizes persisted %d deliveries", got)
		}
		var failed int
		if err := pool.QueryRow(t.Context(), `select count(*) from webhook_deliveries where state='failed' and error_code='invalid_payload'`).Scan(&failed); err != nil || failed != 3 {
			t.Fatalf("failed=%d err=%v", failed, err)
		}
	})

	t.Run("push validation coalescing and duplicate", func(t *testing.T) {
		store, pool := webhookStore(t)
		seedWebhookRepository(t, store, 101)
		processor := NewGitHubProcessor(store, nil)
		for _, delivery := range []Delivery{
			{ID: "push-a", Event: "push", Body: pushBody(10, 101, "refs/heads/main", webhookSHAA)},
			{ID: "push-b", Event: "push", Body: pushBodyWithSize(10, 101, "refs/heads/main", webhookSHAB, 2048)},
			{ID: "push-other", Event: "push", Body: pushBody(10, 101, "refs/heads/other", webhookSHAA)},
		} {
			if inserted, err := processor.Process(t.Context(), delivery); err != nil || !inserted {
				t.Fatalf("%s: inserted=%v err=%v", delivery.ID, inserted, err)
			}
		}
		if inserted, err := processor.Process(t.Context(), Delivery{ID: "push-b", Event: "push", Body: pushBody(10, 101, "refs/heads/main", webhookSHAA)}); err != nil || inserted {
			t.Fatalf("duplicate: inserted=%v err=%v", inserted, err)
		}
		var desired, target string
		var sizeBytes int64
		var jobs int
		if err := pool.QueryRow(t.Context(), `select coalesce(max(desired_sha), ''), coalesce(max(target_sha), ''), max(size_bytes), count(index_jobs.id)
			from repositories left join index_jobs on index_jobs.repository_id=repositories.id where repositories.github_id=101`).Scan(&desired, &target, &sizeBytes, &jobs); err != nil || desired != webhookSHAB || target != webhookSHAB || sizeBytes != 2048*1024 || jobs != 1 {
			t.Fatalf("desired=%q target=%q size=%d jobs=%d err=%v", desired, target, sizeBytes, jobs, err)
		}

		before := deliveryCount(t, pool)
		for index, body := range [][]byte{
			pushBody(0, 101, "refs/heads/main", webhookSHAA),
			pushBody(10, 0, "refs/heads/main", webhookSHAA),
			pushBody(10, 101, "main", webhookSHAA),
			pushBody(10, 101, "refs/heads/", webhookSHAA),
			pushBody(10, 101, "refs/heads/main", "bad"),
			pushBody(10, 101, "refs/heads/main", "0000000000000000000000000000000000000000"),
		} {
			_, err := processor.Process(t.Context(), Delivery{ID: fmt.Sprintf("malformed-%d", index), Event: "push", Body: body})
			var invalid InvalidDeliveryError
			if !errors.As(err, &invalid) {
				t.Fatalf("case %d: err=%v", index, err)
			}
		}
		if got := deliveryCount(t, pool); got != before+6 {
			t.Fatalf("malformed deliveries: before=%d after=%d", before, got)
		}
		var failed int
		if err := pool.QueryRow(t.Context(), `select count(*) from webhook_deliveries where state='failed' and error_code='invalid_payload'`).Scan(&failed); err != nil || failed != 6 {
			t.Fatalf("failed=%d err=%v", failed, err)
		}
		if inserted, err := processor.Process(t.Context(), Delivery{ID: "malformed-0", Event: "push", Body: pushBody(10, 101, "refs/heads/main", webhookSHAA)}); err != nil || inserted {
			t.Fatalf("malformed duplicate: inserted=%v err=%v", inserted, err)
		}
	})

	t.Run("callback failure rolls back receipt and mutation", func(t *testing.T) {
		store, pool := webhookStore(t)
		seedWebhookRepository(t, store, 101)
		if _, err := pool.Exec(t.Context(), `create function reject_index_job() returns trigger language plpgsql as $$ begin raise exception 'reject index job'; end $$;
			create trigger reject_index_job before insert on index_jobs for each row execute function reject_index_job()`); err != nil {
			t.Fatal(err)
		}
		inserted, err := NewGitHubProcessor(store, nil).Process(t.Context(), Delivery{ID: "rollback", Event: "push", Body: pushBodyWithSize(10, 101, "refs/heads/main", webhookSHAA, 2048)})
		if err == nil || inserted {
			t.Fatalf("inserted=%v err=%v", inserted, err)
		}
		var desired string
		var sizeBytes int64
		if err := pool.QueryRow(t.Context(), "select coalesce(desired_sha, ''), size_bytes from repositories where github_id=101").Scan(&desired, &sizeBytes); err != nil || desired != "" || sizeBytes != 1024 || deliveryCount(t, pool) != 0 {
			t.Fatalf("desired=%q size=%d deliveries=%d err=%v", desired, sizeBytes, deliveryCount(t, pool), err)
		}
	})

	t.Run("repository mutations use numeric IDs", func(t *testing.T) {
		store, pool := webhookStore(t)
		for _, id := range []int64{101, 102, 103, 104} {
			seedWebhookRepository(t, store, id)
		}
		processor := NewGitHubProcessor(store, nil)
		for _, delivery := range []Delivery{
			{ID: "rename", Event: "repository", Body: []byte(`{"action":"renamed","installation":{"id":10,"extra":true},"repository":{"id":101,"name":"new","clone_url":"clone-new","html_url":"web-new","owner":{"login":"renamed"},"extra":"GitHub"},"sender":{"login":"ignored"}}`)},
			{ID: "delete", Event: "repository", Body: []byte(`{"action":"deleted","installation":{"id":10},"repository":{"id":102,"name":"wrong-name"}}`)},
			{ID: "archive", Event: "repository", Body: []byte(`{"action":"archived","installation":{"id":10},"repository":{"id":103}}`)},
			{ID: "removed", Event: "installation_repositories", Body: []byte(`{"action":"removed","installation":{"id":10},"repositories_removed":[{"id":104,"name":"wrong-name"}]}`)},
		} {
			if inserted, err := processor.Process(t.Context(), delivery); err != nil || !inserted {
				t.Fatalf("%s: inserted=%v err=%v", delivery.ID, inserted, err)
			}
		}
		var owner, name, cloneURL, webURL string
		if err := pool.QueryRow(t.Context(), "select owner, name, clone_url, web_url from repositories where github_id=101").Scan(&owner, &name, &cloneURL, &webURL); err != nil || owner != "renamed" || name != "new" || cloneURL != "clone-new" || webURL != "web-new" {
			t.Fatalf("rename=%s/%s %s %s err=%v", owner, name, cloneURL, webURL, err)
		}
		rows, err := pool.Query(t.Context(), "select github_id, enabled, error_code from repositories where github_id=any($1) order by github_id", []int64{102, 103, 104})
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		want := map[int64]string{102: "deleted", 103: "archived", 104: "removed"}
		seen := 0
		for rows.Next() {
			var id int64
			var enabled bool
			var code string
			if err := rows.Scan(&id, &enabled, &code); err != nil || enabled || code != want[id] {
				t.Fatalf("id=%d enabled=%v code=%q err=%v", id, enabled, code, err)
			}
			seen++
		}
		if err := rows.Err(); err != nil || seen != len(want) {
			t.Fatalf("disabled repositories=%d err=%v", seen, err)
		}
	})

	for _, action := range []string{"suspend", "deleted"} {
		t.Run("installation "+action+" disables repositories", func(t *testing.T) {
			store, pool := webhookStore(t)
			seedWebhookRepository(t, store, 101)
			body := []byte(fmt.Sprintf(`{"action":%q,"installation":{"id":10}}`, action))
			if inserted, err := NewGitHubProcessor(store, nil).Process(t.Context(), Delivery{ID: action, Event: "installation", Body: body}); err != nil || !inserted {
				t.Fatalf("inserted=%v err=%v", inserted, err)
			}
			var status string
			var enabled bool
			if err := pool.QueryRow(t.Context(), `select installations.status, repositories.enabled from installations join repositories on repositories.installation_id=installations.id where repositories.github_id=101`).Scan(&status, &enabled); err != nil || status != map[string]string{"suspend": "suspended", "deleted": "deleted"}[action] || enabled {
				t.Fatalf("status=%q enabled=%v err=%v", status, enabled, err)
			}
		})
	}

	t.Run("accepted delivery does not wait for reconciliation", func(t *testing.T) {
		store, pool := webhookStore(t)
		started := make(chan struct{})
		release := make(chan struct{})
		reconciler := webhookReconciler(t, store, func(writer http.ResponseWriter) {
			close(started)
			<-release
			fmt.Fprint(writer, `[{"id":10,"account":{"login":"acme","type":"Organization"},"suspended_at":"2026-07-20T00:00:00Z"}]`)
		})
		reconcileRequests := make(chan int64, 1)
		processor := NewGitHubProcessor(store, reconcileRequests)
		delivery := Delivery{ID: "reconcile", Event: "installation", Body: []byte(`{"action":"created","installation":{"id":10}}`)}
		processed := make(chan error, 1)
		go func() {
			inserted, err := processor.Process(t.Context(), delivery)
			if err == nil && !inserted {
				err = errors.New("delivery was not inserted")
			}
			processed <- err
		}()
		deadline := time.Now().Add(time.Second)
		for deliveryCount(t, pool) != 1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		select {
		case err := <-processed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(50 * time.Millisecond):
			close(release)
			<-processed
			t.Fatal("accepted delivery waited for reconciliation")
		}

		select {
		case <-started:
			t.Fatal("webhook processor started reconciliation")
		default:
		}
		select {
		case installationID := <-reconcileRequests:
			if installationID != 10 {
				t.Fatalf("reconcile installation = %d", installationID)
			}
		default:
			t.Fatal("accepted delivery did not schedule reconciliation")
		}
		go func() { processed <- reconciler.All(t.Context()) }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("background reconciliation did not start")
		}
		close(release)
		if err := <-processed; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("reconciliation failure remains retryable", func(t *testing.T) {
		store, _ := webhookStore(t)
		requests := 0
		reconciler := webhookReconciler(t, store, func(writer http.ResponseWriter) {
			requests++
			if requests == 1 {
				http.Error(writer, "down", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(writer, `[{"id":10,"account":{"login":"acme","type":"Organization"},"suspended_at":"2026-07-20T00:00:00Z"}]`)
		})
		processor := NewGitHubProcessor(store, nil)
		delivery := Delivery{ID: "retry-reconcile", Event: "installation", Body: []byte(`{"action":"created","installation":{"id":10}}`)}
		if inserted, err := processor.Process(t.Context(), delivery); err != nil || !inserted {
			t.Fatalf("accepted: inserted=%v err=%v", inserted, err)
		}
		if err := reconciler.All(t.Context()); err == nil {
			t.Fatal("first background reconciliation succeeded")
		}
		if err := reconciler.All(t.Context()); err != nil {
			t.Fatal(err)
		}
		if requests != 2 {
			t.Fatalf("reconcile requests=%d", requests)
		}
	})
}

func TestGitHubProcessorRecordsTerminalMetricsOnce(t *testing.T) {
	store, _ := webhookStore(t)
	seedWebhookRepository(t, store, 101)
	metrics := observability.New()
	processor := NewGitHubProcessor(store, nil, metrics)
	accepted := Delivery{ID: "accepted", Event: "push", Body: pushBody(10, 101, "refs/heads/main", webhookSHAA)}
	ignored := Delivery{ID: "ignored", Event: "private-event", Body: []byte(`{"secret":"not-a-label"}`)}
	for _, delivery := range []Delivery{accepted, ignored, ignored} {
		if _, err := processor.Process(t.Context(), delivery); err != nil {
			t.Fatal(err)
		}
	}

	body := scrapeWebhookMetrics(t, metrics)
	for _, want := range []string{
		`grepnest_webhook_deliveries_total{event="push",result="accepted"} 1`,
		`grepnest_webhook_deliveries_total{event="unknown",result="ignored"} 1`,
		`grepnest_webhook_deliveries_total{event="unknown",result="duplicate"} 1`,
	} {
		if strings.Count(body, want) != 1 {
			t.Errorf("metric %q not recorded exactly once:\n%s", want, body)
		}
	}
	if strings.Contains(body, "private-event") || strings.Contains(body, "not-a-label") {
		t.Fatalf("metrics expose unknown event data:\n%s", body)
	}
}

func scrapeWebhookMetrics(t *testing.T, metrics *observability.Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder.Body.String()
}

func pushBody(installationID, repositoryID int64, ref, sha string) []byte {
	return pushBodyWithSize(installationID, repositoryID, ref, sha, 1)
}

func pushBodyWithSize(installationID, repositoryID int64, ref, sha string, sizeKB int64) []byte {
	return []byte(fmt.Sprintf(`{"installation":{"id":%d},"repository":{"id":%d,"size":%d,"extra":"GitHub"},"ref":%q,"after":%q,"sender":{"login":"ignored"}}`, installationID, repositoryID, sizeKB, ref, sha))
}

func seedWebhookRepository(t *testing.T, store *postgres.Store, id int64) {
	t.Helper()
	if err := store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{GitHubID: id, InstallationID: 10, SizeBytes: 1024, Owner: "acme", Name: fmt.Sprintf("repo-%d", id), CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func deliveryCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), "select count(*) from webhook_deliveries").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func webhookReconciler(t *testing.T, store *postgres.Store, respond func(http.ResponseWriter)) *githubapp.Reconciler {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		respond(writer)
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := githubapp.NewSigner(1, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := githubapp.NewClient(githubapp.Endpoints{Web: endpoint, API: endpoint, Upload: endpoint, Git: endpoint}, server.Client(), signer, "2022-11-28", 1024, time.Now)
	return githubapp.NewReconciler(client, store)
}

func webhookStore(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("GREPNEST_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GREPNEST_TEST_POSTGRES_DSN is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("grepnest_delivery_%x", time.Now().UnixNano())
	if _, err := admin.Exec(t.Context(), "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "drop schema "+schema+" cascade") })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		_, err := connection.Exec(ctx, "set search_path to "+schema)
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return postgres.New(pool), pool
}
