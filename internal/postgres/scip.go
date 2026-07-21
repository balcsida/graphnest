package postgres

import (
	"context"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/scipgraph"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ReplaceSCIP(ctx context.Context, repositoryID int64, commit string, upload scipgraph.Upload) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := tx.QueryRow(ctx, `select id from repositories where id=$1 and indexed_sha=$2 for update`, repositoryID, commit).Scan(&repositoryID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from scip_uploads where repository_id=$1`, repositoryID); err != nil {
		return err
	}
	var uploadID int64
	if err := tx.QueryRow(ctx, `insert into scip_uploads
		(repository_id, commit, project_root, indexer_name, indexer_version)
		values ($1, $2, $3, $4, $5) returning id`, repositoryID, commit, upload.ProjectRoot, upload.IndexerName, upload.IndexerVersion).Scan(&uploadID); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"scip_occurrences"},
		[]string{"upload_id", "path", "start_line", "start_character", "end_line", "end_character", "symbol", "roles", "local"},
		pgx.CopyFromSlice(len(upload.Occurrences), func(index int) ([]any, error) {
			occurrence := upload.Occurrences[index]
			return []any{uploadID, occurrence.Path, occurrence.StartLine, occurrence.StartCharacter, occurrence.EndLine, occurrence.EndCharacter, occurrence.Symbol, occurrence.Roles, occurrence.Local}, nil
		})); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"scip_relationships"},
		[]string{"upload_id", "source_symbol", "target_symbol", "is_definition", "is_reference", "is_implementation", "is_type_definition"},
		pgx.CopyFromSlice(len(upload.Relationships), func(index int) ([]any, error) {
			relationship := upload.Relationships[index]
			return []any{uploadID, relationship.Source, relationship.Target, relationship.Definition, relationship.Reference, relationship.Implementation, relationship.TypeDefinition}, nil
		})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) OccurrenceAt(ctx context.Context, repositoryID int64, commit, path string, line, character int) (scipgraph.StoredOccurrence, error) {
	var occurrence scipgraph.StoredOccurrence
	err := s.pool.QueryRow(ctx, `select uploads.id, uploads.repository_id, uploads.commit,
		occurrences.path, occurrences.start_line, occurrences.start_character,
		occurrences.end_line, occurrences.end_character, occurrences.symbol,
		occurrences.roles, occurrences.local
		from scip_uploads uploads
		join repositories on repositories.id=uploads.repository_id and repositories.indexed_sha=uploads.commit
		join scip_occurrences occurrences on occurrences.upload_id=uploads.id
		where uploads.repository_id=$1 and uploads.commit=$2 and occurrences.path=$3
		and (occurrences.start_line<$4 or occurrences.start_line=$4 and occurrences.start_character<=$5)
		and (occurrences.end_line>$4 or occurrences.end_line=$4 and occurrences.end_character>$5)
		order by occurrences.start_line desc, occurrences.start_character desc,
			occurrences.end_line, occurrences.end_character
		limit 1`, repositoryID, commit, path, line, character).Scan(
		&occurrence.UploadID, &occurrence.RepositoryID, &occurrence.Commit,
		&occurrence.Path, &occurrence.StartLine, &occurrence.StartCharacter,
		&occurrence.EndLine, &occurrence.EndCharacter, &occurrence.Symbol,
		&occurrence.Roles, &occurrence.Local)
	return occurrence, err
}

func (s *Store) Locations(ctx context.Context, principal authn.Principal, origin scipgraph.StoredOccurrence, operation string, max int) ([]scipgraph.Location, bool, error) {
	if len(principal.RepositoryIDs) == 0 {
		return []scipgraph.Location{}, false, nil
	}
	rows, err := s.pool.Query(ctx, `with authorized_uploads as (
		select uploads.id, repositories.github_id, repositories.owner || '/' || repositories.name repository_name, uploads.commit
		from scip_uploads uploads
		join repositories on repositories.id=uploads.repository_id and repositories.indexed_sha=uploads.commit
		join installations on installations.id=repositories.installation_id
		where installations.github_id=$1 and repositories.github_id=any($2)
		and installations.status='active' and repositories.enabled and not repositories.archived
	), targets as (
		select case when $5 then $4::text || ':' || $3 else $3 end symbol
		where $6 in ('definitions', 'references')
		union
		select case when left(relationships.target_symbol, 6)='local '
			then relationships.upload_id::text || ':' || relationships.target_symbol
			else relationships.target_symbol end
		from scip_relationships relationships
		join authorized_uploads on authorized_uploads.id=relationships.upload_id
		where $6='implementations' and relationships.is_implementation
		and (case when left(relationships.source_symbol, 6)='local '
			then relationships.upload_id::text || ':' || relationships.source_symbol
			else relationships.source_symbol end)=case when $5 then $4::text || ':' || $3 else $3 end
	)
	select authorized_uploads.github_id, authorized_uploads.repository_name, authorized_uploads.commit,
		occurrences.path, occurrences.start_line, occurrences.start_character,
		occurrences.end_line, occurrences.end_character, occurrences.symbol, occurrences.roles
	from scip_occurrences occurrences
	join authorized_uploads on authorized_uploads.id=occurrences.upload_id
	join targets on targets.symbol=case when occurrences.local
		then occurrences.upload_id::text || ':' || occurrences.symbol else occurrences.symbol end
	where case $6 when 'definitions' then occurrences.roles & 1 <> 0
		when 'references' then occurrences.roles & 1 = 0
		when 'implementations' then occurrences.roles & 1 <> 0
		else false end
	order by authorized_uploads.github_id, occurrences.path, occurrences.start_line, occurrences.start_character
	limit $7`, principal.InstallationID, principal.RepositoryIDs, origin.Symbol, origin.UploadID, origin.Local, operation, max+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	locations := make([]scipgraph.Location, 0, max+1)
	for rows.Next() {
		var location scipgraph.Location
		if err := rows.Scan(&location.RepositoryID, &location.RepositoryName, &location.Commit,
			&location.Path, &location.StartLine, &location.StartCharacter, &location.EndLine,
			&location.EndCharacter, &location.Symbol, &location.Roles); err != nil {
			return nil, false, err
		}
		locations = append(locations, location)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(locations) > max
	if truncated {
		locations = locations[:max]
	}
	return locations, truncated, nil
}
