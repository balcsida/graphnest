//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaName = regexp.MustCompile(`^grepnest_test_[0-9a-f]{16}$`)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("GREPNEST_TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("GREPNEST_REQUIRE_POSTGRES") == "1" {
			t.Fatal("GREPNEST_TEST_POSTGRES_DSN is not set")
		}
		t.Skip("GREPNEST_TEST_POSTGRES_DSN is not set")
	}

	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	schema := "grepnest_test_" + hex.EncodeToString(bytes)
	if !schemaName.MatchString(schema) {
		t.Fatalf("invalid schema name %q", schema)
	}
	if _, err := admin.Exec(t.Context(), "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(t.Context(), "drop schema "+schema+" cascade")
	})
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
	return pool
}

func TestMigrateIsConcurrentAndIdempotent(t *testing.T) {
	pool := testPool(t)
	errors := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() { defer group.Done(); errors <- Migrate(t.Context(), pool) }()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"installations", "repositories", "webhook_deliveries", "index_jobs", "search_nodes"} {
		var found bool
		if err := pool.QueryRow(t.Context(), `select to_regclass($1) is not null`, name).Scan(&found); err != nil || !found {
			t.Fatalf("relation %s: found=%v err=%v", name, found, err)
		}
	}
	var count int
	if err := pool.QueryRow(t.Context(), `select count(*) from schema_migrations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migrations=%d err=%v", count, err)
	}
}

func TestIndexJobLeaseConstraint(t *testing.T) {
	pool := testPool(t)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}

	var installationID int64
	if err := pool.QueryRow(t.Context(), `
		insert into installations (github_id, account_login, account_type, status)
		values (1, 'test', 'User', 'active') returning id`).Scan(&installationID); err != nil {
		t.Fatal(err)
	}
	nextRepositoryID := int64(1)
	newRepository := func() int64 {
		nextRepositoryID++
		var repositoryID int64
		if err := pool.QueryRow(t.Context(), `
			insert into repositories (
				github_id, installation_id, owner, name, clone_url, web_url,
				default_branch, private, archived, enabled, status
			) values ($1, $2, 'owner', $3, 'https://example.invalid/clone',
				'https://example.invalid/web', 'main', false, false, true, 'pending')
			returning id`, nextRepositoryID, installationID, fmt.Sprintf("repository-%d", nextRepositoryID)).Scan(&repositoryID); err != nil {
			t.Fatal(err)
		}
		return repositoryID
	}
	expectRejected := func(state string, owner, expires any) {
		t.Helper()
		_, err := pool.Exec(t.Context(), `
			insert into index_jobs (repository_id, target_sha, state, lease_owner, lease_expires_at)
			values ($1, repeat('a', 40), $2, $3, $4)`, newRepository(), state, owner, expires)
		if err == nil {
			t.Fatalf("state %q accepted incomplete lease", state)
		}
	}

	for _, state := range []string{"queued", "succeeded", "failed", "superseded"} {
		expectRejected(state, "worker", nil)
		expectRejected(state, nil, time.Now())
	}
	expectRejected("running", "worker", nil)
	expectRejected("running", nil, time.Now())
	if _, err := pool.Exec(t.Context(), `
		insert into index_jobs (repository_id, target_sha, state, lease_owner, lease_expires_at)
		values ($1, repeat('a', 40), 'running', 'worker', now())`, newRepository()); err != nil {
		t.Fatal(err)
	}
}
