package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLockKey int64 = 2651002

type migration struct {
	name    string
	version int64
}

func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `create table if not exists schema_migrations (version bigint primary key)`); err != nil {
		return err
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	migrations, err := migrationDescriptors(entries)
	if err != nil {
		return err
	}
	backfillSCIP := false
	for _, migration := range migrations {
		version := migration.version
		var applied bool
		if err := tx.QueryRow(ctx, "select exists(select 1 from schema_migrations where version = $1)", version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationFiles.ReadFile("migrations/" + migration.name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %q: %w", migration.name, err)
		}
		if version == 19 {
			backfillSCIP = true
		}
		if _, err := tx.Exec(ctx, "insert into schema_migrations (version) values ($1)", version); err != nil {
			return err
		}
	}
	// The Go backfill uses the current storage schema, so apply all DDL first.
	if backfillSCIP {
		if err := backfillLegacySCIPGraphs(ctx, tx); err != nil {
			return fmt.Errorf("legacy SCIP backfill: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func backfillLegacySCIPGraphs(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `select scip.repository_id, scip.id, scip.commit
		from scip_uploads scip
		join repositories on repositories.id=scip.repository_id and repositories.indexed_sha=scip.commit
		left join graph_uploads graph on graph.repository_id=scip.repository_id and graph.active
		where graph.id is null or graph.source='scip'
		order by scip.repository_id`)
	if err != nil {
		return err
	}
	type upload struct {
		repositoryID, uploadID int64
		commit                 string
	}
	uploads := []upload{}
	for rows.Next() {
		var item upload
		if err := rows.Scan(&item.repositoryID, &item.uploadID, &item.commit); err != nil {
			rows.Close()
			return err
		}
		uploads = append(uploads, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	store := New(nil)
	for _, item := range uploads {
		artifact, err := store.scipArtifact(ctx, tx, item.repositoryID, item.uploadID, item.commit)
		if err != nil {
			return err
		}
		if _, err := replaceGraph(ctx, tx, item.repositoryID, GraphSourceSCIP, artifact); err != nil {
			return err
		}
	}
	return nil
}

func migrationDescriptors(entries []fs.DirEntry) ([]migration, error) {
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration %q: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{name: entry.Name(), version: version})
	}
	sort.SliceStable(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for index := 1; index < len(migrations); index++ {
		if migrations[index-1].version == migrations[index].version {
			return nil, fmt.Errorf("duplicate migration version %d: %q and %q", migrations[index].version, migrations[index-1].name, migrations[index].name)
		}
	}
	return migrations, nil
}
