//go:build integration

package postgres

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

func TestOAuthRequestLimitsSharedByStores(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		limit    int
	}{
		{"/oauth/register", 10},
		{"/oauth/authorize", 10},
		{"/oauth/token", 60},
		{"/oauth/revoke", 60},
	} {
		t.Run(test.endpoint, func(t *testing.T) {
			store := migratedStore(t)
			stores := []*Store{store, New(store.pool)}
			now := time.Now().UTC().Truncate(time.Minute)
			allowed := concurrentOAuthRequests(t, stores, 80, func(i int) string {
				if i%2 == 0 {
					return fmt.Sprintf("192.0.2.1:%d", 10000+i)
				}
				return fmt.Sprintf("[::ffff:192.0.2.1]:%d", 10000+i)
			}, test.endpoint, now)
			if allowed != test.limit {
				t.Fatalf("accepted %d requests for one source across stores, want %d", allowed, test.limit)
			}
			if !allowOAuthRequest(t, stores[1], "192.0.2.1:9999", test.endpoint, now.Add(time.Minute)) {
				t.Fatal("new minute did not restore the source budget")
			}
			var rows int
			if err := store.pool.QueryRow(t.Context(), `select count(*) from oauth_request_limits`).Scan(&rows); err != nil || rows != 2 {
				t.Fatalf("expired source buckets were not removed: rows=%d err=%v", rows, err)
			}
		})
	}
}

func TestOAuthRequestLimitsBoundDistinctSources(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		limit    int
	}{
		{"/oauth/register", 100},
		{"/oauth/authorize", 100},
		{"/oauth/token", 1000},
		{"/oauth/revoke", 1000},
	} {
		t.Run(test.endpoint, func(t *testing.T) {
			store := migratedStore(t)
			now := time.Now().UTC().Truncate(time.Minute)
			allowed := concurrentOAuthRequests(t, []*Store{store, New(store.pool)}, test.limit+20, func(i int) string {
				return fmt.Sprintf("[2001:db8::%x]:10000", i+1)
			}, test.endpoint, now)
			if allowed != test.limit {
				t.Fatalf("accepted %d distinct sources, want deployment limit %d", allowed, test.limit)
			}
			var rows int
			if err := store.pool.QueryRow(t.Context(), `select count(*) from oauth_request_limits`).Scan(&rows); err != nil || rows > test.limit+1 {
				t.Fatalf("limiter storage is unbounded: rows=%d err=%v", rows, err)
			}
		})
	}
}

func TestOAuthAuthorizationLimiterMigrationUpgradesV24(t *testing.T) {
	pool := testPool(t)
	if err := migrateThrough(t.Context(), pool, 24); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	if !allowOAuthRequest(t, New(pool), "192.0.2.1:10000", "/oauth/token", now) {
		t.Fatal("token request denied")
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `insert into oauth_request_limits(endpoint,source_hash,window_start,request_count)
		values('/oauth/authorize',''::bytea,$1,1)`, now); err != nil {
		t.Fatalf("upgraded schema rejects authorization budgets: %v", err)
	}
	var tokenRows int
	if err := pool.QueryRow(t.Context(), `select count(*) from oauth_request_limits where endpoint='/oauth/token'`).Scan(&tokenRows); err != nil || tokenRows != 2 {
		t.Fatalf("migration lost existing budgets: rows=%d err=%v", tokenRows, err)
	}
	if _, err := pool.Exec(t.Context(), `insert into oauth_request_limits(endpoint,source_hash,window_start,request_count)
		values('/oauth/unknown',''::bytea,$1,1)`, now); err == nil {
		t.Fatal("schema accepted an unsupported endpoint")
	}
}

func concurrentOAuthRequests(t *testing.T, stores []*Store, count int, remote func(int) string, endpoint string, now time.Time) int {
	t.Helper()
	var allowed atomic.Int64
	var next atomic.Int64
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= count {
					return
				}
				if allowOAuthRequest(t, stores[worker%len(stores)], remote(i), endpoint, now) {
					allowed.Add(1)
				}
			}
		})
	}
	workers.Wait()
	return int(allowed.Load())
}

func allowOAuthRequest(t *testing.T, store *Store, remote, endpoint string, now time.Time) bool {
	t.Helper()
	allowed, err := store.AllowOAuthRequest(t.Context(), remote, endpoint, now)
	if err != nil {
		t.Errorf("rate limit: %v", err)
	}
	return allowed
}

func TestOAuthClientRegistrationQuotaIsAtomic(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC()
	if _, err := store.pool.Exec(t.Context(), `insert into oauth_clients(id,client_name,redirect_uris,created_at,last_used_at)
		select 'gnc_seed_' || i, 'client', array['http://127.0.0.1/callback'], $1, $1 from generate_series(1,9999) i`, now); err != nil {
		t.Fatal(err)
	}
	stores := []*Store{store, New(store.pool)}
	var workers sync.WaitGroup
	var accepted atomic.Int64
	for i := range 16 {
		workers.Go(func() {
			client := authn.OAuthClient{ID: fmt.Sprintf("gnc_new_%d", i), Name: "client", RedirectURIs: []string{"http://127.0.0.1/callback"}, CreatedAt: now}
			if err := stores[i%len(stores)].CreateOAuthClient(t.Context(), client); err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, authn.ErrOAuthClientQuota) {
				t.Errorf("registration: %v", err)
			}
		})
	}
	workers.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d registrations with one quota slot left", accepted.Load())
	}
	var count int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from oauth_clients`).Scan(&count); err != nil || count != 10000 {
		t.Fatalf("clients=%d err=%v", count, err)
	}
}
