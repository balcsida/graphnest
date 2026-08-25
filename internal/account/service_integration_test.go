//go:build integration

package account_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/account"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCreateAdminTokenUsesLiveGlobalCeiling(t *testing.T) {
	// Mutation caught: replacing the real authorizer with principal.RepositoryIDs rejects this admin.
	store, pool := accountStore(t)
	for _, id := range []int64{101, 102} {
		if err := store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{GitHubID: id, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{GitHubID: id, InstallationID: id, Owner: "acme", Name: strconv.FormatInt(id, 10), CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	var userID int64
	if err := pool.QueryRow(t.Context(), `insert into users (external_id,user_name,source) values ('account-admin','admin','scim') returning id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `insert into user_roles (user_id,administrator) values ($1,true)`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	manager := authn.TokenManager{Store: store, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}
	service := account.Service{Manager: manager, Authorizer: authz.NewPostgres(store)}
	expires := now.Add(time.Hour)
	_, plaintext, err := service.CreateToken(t.Context(), authn.Principal{Subject: strconv.FormatInt(userID, 10), Method: "oidc", Administrator: true}, &expires, []int64{102})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := manager.Authenticate(t.Context(), plaintext)
	if err != nil || !principal.Administrator || principal.Method != "api_token" || len(principal.RepositoryIDs) != 1 || principal.RepositoryIDs[0] != 102 {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	authorizer := authz.NewPostgres(store)
	if _, err := authorizer.AuthorizedRepository(t.Context(), principal, 101); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("repo101=%v", err)
	}
	if _, err := authorizer.AuthorizedRepository(t.Context(), principal, 102); err != nil {
		t.Fatal(err)
	}
}

func accountStore(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("GRAPHNEST_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GRAPHNEST_TEST_POSTGRES_DSN is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	schema := "graphnest_account_" + hex.EncodeToString(b)
	if _, err := admin.Exec(t.Context(), "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "drop schema "+schema+" cascade") })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "set search_path to "+schema)
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
