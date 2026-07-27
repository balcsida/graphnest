package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/jackc/pgx/v5"
)

type GraphSource string

const (
	GraphSourceManaged  GraphSource = "managed"
	GraphSourceExternal GraphSource = "external"
)

type GraphUpload struct {
	ID, RepositoryID                    int64
	Commit                              string
	SchemaVersion, NodeCount, EdgeCount int
	Source                              GraphSource
}

type GraphReplacement struct {
	Upload  GraphUpload
	Applied bool
}

type GraphJob struct {
	ID, RepositoryID     int64
	TargetSHA            string
	State, LeaseOwner    string
	Attempt, MaxAttempts int
	LeaseExpiresAt       time.Time
}

func (s *Store) ReplaceGraph(ctx context.Context, repositoryID int64, source GraphSource, artifact graphartifact.Artifact) (GraphReplacement, error) {
	if artifact.RepositoryID != repositoryID || (source != GraphSourceManaged && source != GraphSourceExternal) || graphartifact.Validate(artifact, graphartifact.Limits{}) != nil {
		return GraphReplacement{}, graphartifact.ErrInvalidArtifact
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return GraphReplacement{}, err
	}
	defer tx.Rollback(ctx)

	replacement, err := replaceGraph(ctx, tx, repositoryID, source, artifact)
	if err != nil {
		return GraphReplacement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GraphReplacement{}, err
	}
	return replacement, nil
}

func replaceGraph(ctx context.Context, tx pgx.Tx, repositoryID int64, source GraphSource, artifact graphartifact.Artifact) (GraphReplacement, error) {
	var indexedSHA string
	if err := tx.QueryRow(ctx, `select coalesce(indexed_sha, '') from repositories where id=$1 for update`, repositoryID).Scan(&indexedSHA); err != nil {
		return GraphReplacement{}, err
	}
	var current GraphUpload
	err := tx.QueryRow(ctx, `select id, repository_id, commit, schema_version, source, node_count, edge_count
		from graph_uploads where repository_id=$1 for update`, repositoryID).Scan(
		&current.ID, &current.RepositoryID, &current.Commit, &current.SchemaVersion, &current.Source, &current.NodeCount, &current.EdgeCount)
	if err != nil && err != pgx.ErrNoRows {
		return GraphReplacement{}, err
	}
	if artifact.Commit != indexedSHA || current.Source == GraphSourceExternal && current.Commit == indexedSHA && source == GraphSourceManaged {
		return GraphReplacement{Upload: current}, nil
	}
	if current.ID != 0 {
		if _, err := tx.Exec(ctx, `delete from graph_uploads where id=$1`, current.ID); err != nil {
			return GraphReplacement{}, err
		}
	}
	var upload GraphUpload
	err = tx.QueryRow(ctx, `insert into graph_uploads
		(repository_id, commit, schema_version, source, analyzer_name, analyzer_version, content_hash, node_count, edge_count)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		returning id, repository_id, commit, schema_version, source, node_count, edge_count`,
		repositoryID, artifact.Commit, artifact.SchemaVersion, source, artifact.Analyzer.Name, artifact.Analyzer.Version,
		artifact.ContentHash, len(artifact.Nodes), len(artifact.Edges)).Scan(
		&upload.ID, &upload.RepositoryID, &upload.Commit, &upload.SchemaVersion, &upload.Source, &upload.NodeCount, &upload.EdgeCount)
	if err != nil {
		return GraphReplacement{}, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"graph_nodes"},
		[]string{"upload_id", "uid", "kind", "path", "language", "symbol_kind", "qualified_name", "signature", "scip_symbol", "start_line", "start_character", "end_line", "end_character"},
		pgx.CopyFromSlice(len(artifact.Nodes), func(index int) ([]any, error) {
			node := artifact.Nodes[index]
			return []any{upload.ID, node.UID, node.Kind, node.Path, node.Language, node.SymbolKind, node.QualifiedName, node.Signature, node.SCIPSymbol,
				node.Range.StartLine, node.Range.StartCharacter, node.Range.EndLine, node.Range.EndCharacter}, nil
		})); err != nil {
		return GraphReplacement{}, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"graph_edges"},
		[]string{"upload_id", "source_uid", "target_uid", "kind", "path", "start_line", "start_character", "end_line", "end_character", "confidence", "resolution_reason"},
		pgx.CopyFromSlice(len(artifact.Edges), func(index int) ([]any, error) {
			edge := artifact.Edges[index]
			return []any{upload.ID, edge.SourceUID, edge.TargetUID, edge.Kind, edge.Path, edge.Range.StartLine, edge.Range.StartCharacter,
				edge.Range.EndLine, edge.Range.EndCharacter, edge.Confidence, edge.ResolutionReason}, nil
		})); err != nil {
		return GraphReplacement{}, err
	}
	return GraphReplacement{Upload: upload, Applied: true}, nil
}

func (s *Store) ClaimGraph(ctx context.Context, owner string) (GraphJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return GraphJob{}, err
	}
	defer tx.Rollback(ctx)
	var job GraphJob
	err = tx.QueryRow(ctx, `
		with next as (
			select j.id from installations
			join repositories on repositories.installation_id=installations.id
			join graph_jobs j on j.repository_id=repositories.id
			where j.state='queued' and j.run_after<=now() and repositories.enabled
			and not repositories.archived and installations.status='active'
			and not exists(select 1 from graph_jobs running where running.repository_id=j.repository_id and running.state='running')
			order by j.run_after, j.id
			for share of installations for update of repositories, j skip locked limit 1
		)
		update graph_jobs set state='running', attempt=attempt+1, lease_owner=$1,
			lease_expires_at=now()+interval '2 minutes', updated_at=now()
		where id=(select id from next)
		returning id, repository_id, target_sha, state, lease_owner, attempt, max_attempts, lease_expires_at`, owner).
		Scan(&job.ID, &job.RepositoryID, &job.TargetSHA, &job.State, &job.LeaseOwner,
			&job.Attempt, &job.MaxAttempts, &job.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return GraphJob{}, ErrNoJob
	}
	if err != nil {
		return GraphJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GraphJob{}, err
	}
	return job, nil
}

func (s *Store) RenewGraphLease(ctx context.Context, id int64, owner string) error {
	result, err := s.pool.Exec(ctx, `update graph_jobs set lease_expires_at=now()+interval '2 minutes', updated_at=now()
		where id=$1 and state='running' and lease_owner=$2 and lease_expires_at>now()`, id, owner)
	return leaseResult(result, err)
}

func (s *Store) CompleteGraph(ctx context.Context, id int64, owner string, artifact graphartifact.Artifact) error {
	if graphartifact.Validate(artifact, graphartifact.Limits{}) != nil {
		return graphartifact.ErrInvalidArtifact
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var repositoryID int64
	var targetSHA, indexedSHA, installationStatus string
	var enabled, archived bool
	if err := tx.QueryRow(ctx, `select repositories.id, graph_jobs.target_sha,
		coalesce(repositories.indexed_sha, ''), repositories.enabled, repositories.archived, installations.status
		from installations join repositories on repositories.installation_id=installations.id
		join graph_jobs on graph_jobs.repository_id=repositories.id
		where graph_jobs.id=$1 and graph_jobs.state='running' and graph_jobs.lease_owner=$2
		and graph_jobs.lease_expires_at>now()
		for share of installations for update of repositories, graph_jobs`, id, owner).
		Scan(&repositoryID, &targetSHA, &indexedSHA, &enabled, &archived, &installationStatus); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return err
	}
	if artifact.RepositoryID != repositoryID || artifact.Commit != targetSHA {
		return graphartifact.ErrInvalidArtifact
	}
	state := "superseded"
	if enabled && !archived && installationStatus == "active" && indexedSHA == targetSHA {
		replacement, err := replaceGraph(ctx, tx, repositoryID, GraphSourceManaged, artifact)
		if err != nil {
			return err
		}
		if replacement.Applied {
			state = "succeeded"
		}
	}
	result, err := tx.Exec(ctx, `update graph_jobs set state=$2, lease_owner=null, lease_expires_at=null,
		error_code=null, error_message=null, updated_at=now() where id=$1`, id, state)
	if err := leaseResult(result, err); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FailGraph(ctx context.Context, id int64, owner, errorCode string, retry bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var targetSHA, indexedSHA, installationStatus string
	var enabled, archived bool
	var attempt, maxAttempts int
	if err := tx.QueryRow(ctx, `select graph_jobs.target_sha, coalesce(repositories.indexed_sha, ''),
		repositories.enabled, repositories.archived, installations.status, graph_jobs.attempt, graph_jobs.max_attempts
		from installations join repositories on repositories.installation_id=installations.id
		join graph_jobs on graph_jobs.repository_id=repositories.id
		where graph_jobs.id=$1 and graph_jobs.state='running' and graph_jobs.lease_owner=$2
		and graph_jobs.lease_expires_at>now()
		for share of installations for update of repositories, graph_jobs`, id, owner).
		Scan(&targetSHA, &indexedSHA, &enabled, &archived, &installationStatus, &attempt, &maxAttempts); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return err
	}
	state := "failed"
	if !enabled || archived || installationStatus != "active" || indexedSHA != targetSHA {
		state = "superseded"
	} else if retry && attempt < maxAttempts {
		state = "queued"
	}
	if _, err := tx.Exec(ctx, `update graph_jobs set state=$2, lease_owner=null, lease_expires_at=null,
		error_code=$3, error_message=null,
		run_after=case when $2::varchar='queued' then now()+interval '1 second'*
			least(5*power(2::double precision, attempt-1), 300)*random() else run_after end,
		updated_at=now() where id=$1`, id, state, errorCode); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ReapExpiredGraph(ctx context.Context, limit int) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	type expiredJob struct {
		id               int64
		target, indexed  string
		attempt, maximum int
		available        bool
	}
	rows, err := tx.Query(ctx, `select j.id, j.target_sha, coalesce(r.indexed_sha, ''), j.attempt,
		j.max_attempts, r.enabled and not r.archived and i.status='active'
		from installations i join repositories r on r.installation_id=i.id
		join graph_jobs j on j.repository_id=r.id
		where j.state='running' and j.lease_expires_at<=now()
		order by j.lease_expires_at, j.id
		for share of i for update of r, j skip locked limit $1`, limit)
	if err != nil {
		return 0, err
	}
	var jobs []expiredJob
	for rows.Next() {
		var job expiredJob
		if err := rows.Scan(&job.id, &job.target, &job.indexed, &job.attempt, &job.maximum, &job.available); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, job := range jobs {
		state := "failed"
		if !job.available || job.indexed != job.target {
			state = "superseded"
		} else if job.attempt < job.maximum {
			state = "queued"
		}
		if _, err := tx.Exec(ctx, `update graph_jobs set state=$2, lease_owner=null, lease_expires_at=null,
			error_code='lease_expired', error_message=null,
			run_after=case when $2::varchar='queued' then now()+interval '1 second'*
				least(5*power(2::double precision, attempt-1), 300)*random() else run_after end,
			updated_at=now() where id=$1`, job.id, state); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(jobs)), nil
}

func (s *Store) ActiveGraphJobIDs(ctx context.Context) (map[int64]struct{}, error) {
	rows, err := s.pool.Query(ctx, `select id from graph_jobs where state='running' and lease_expires_at>now()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]struct{}{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = struct{}{}
	}
	return result, rows.Err()
}

func (s *Store) GraphQueueDepths(ctx context.Context) (map[string]int64, error) {
	result := map[string]int64{"queued": 0, "running": 0, "succeeded": 0, "failed": 0, "superseded": 0}
	rows, err := s.pool.Query(ctx, `select state, count(*) from graph_jobs
		where state in ('queued','running','succeeded','failed','superseded') group by state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		result[state] = count
	}
	return result, rows.Err()
}

func (s *Store) LoadGraph(ctx context.Context, uploadID int64) (graphartifact.Artifact, error) {
	var artifact graphartifact.Artifact
	err := s.pool.QueryRow(ctx, `select schema_version, repository_id, commit, analyzer_name, analyzer_version, content_hash
		from graph_uploads where id=$1`, uploadID).Scan(&artifact.SchemaVersion, &artifact.RepositoryID, &artifact.Commit,
		&artifact.Analyzer.Name, &artifact.Analyzer.Version, &artifact.ContentHash)
	if err != nil {
		return graphartifact.Artifact{}, err
	}
	nodes, err := s.pool.Query(ctx, `select uid, kind, path, language, symbol_kind, qualified_name, signature, scip_symbol,
		start_line, start_character, end_line, end_character from graph_nodes where upload_id=$1 order by id`, uploadID)
	if err != nil {
		return graphartifact.Artifact{}, err
	}
	defer nodes.Close()
	for nodes.Next() {
		var node graphartifact.Node
		var kind int
		if err := nodes.Scan(&node.UID, &kind, &node.Path, &node.Language, &node.SymbolKind, &node.QualifiedName, &node.Signature, &node.SCIPSymbol,
			&node.Range.StartLine, &node.Range.StartCharacter, &node.Range.EndLine, &node.Range.EndCharacter); err != nil {
			return graphartifact.Artifact{}, err
		}
		node.Kind = graphartifact.NodeKind(kind)
		artifact.Nodes = append(artifact.Nodes, node)
	}
	if err := nodes.Err(); err != nil {
		return graphartifact.Artifact{}, err
	}
	edges, err := s.pool.Query(ctx, `select source_uid, target_uid, kind, path, start_line, start_character, end_line, end_character,
		confidence, resolution_reason from graph_edges where upload_id=$1 order by id`, uploadID)
	if err != nil {
		return graphartifact.Artifact{}, err
	}
	defer edges.Close()
	for edges.Next() {
		var edge graphartifact.Edge
		var kind int
		if err := edges.Scan(&edge.SourceUID, &edge.TargetUID, &kind, &edge.Path, &edge.Range.StartLine, &edge.Range.StartCharacter,
			&edge.Range.EndLine, &edge.Range.EndCharacter, &edge.Confidence, &edge.ResolutionReason); err != nil {
			return graphartifact.Artifact{}, err
		}
		edge.Kind = graphartifact.EdgeKind(kind)
		artifact.Edges = append(artifact.Edges, edge)
	}
	if err := edges.Err(); err != nil {
		return graphartifact.Artifact{}, err
	}
	return artifact, nil
}
