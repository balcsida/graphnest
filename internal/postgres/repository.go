package postgres

import (
	"context"
	"time"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/jackc/pgx/v5"
)

func (s *Store) AdminOverview(ctx context.Context, installationID int64, repositoryIDs []int64) (admin.Overview, error) {
	r := admin.Overview{Repositories: map[string]int64{}, Jobs: map[string]int64{}, Deliveries: map[string]int64{}}
	for query, target := range map[string]map[string]int64{
		`select repositories.status,count(*) from repositories join installations on installations.id=repositories.installation_id
			where installations.github_id=$1 and repositories.github_id=any($2) group by repositories.status`: r.Repositories,
		`select jobs.state,count(*) from index_jobs jobs join repositories on repositories.id=jobs.repository_id
			join installations on installations.id=repositories.installation_id where installations.github_id=$1
			and repositories.github_id=any($2) group by jobs.state`: r.Jobs,
		`select deliveries.state,count(*) from webhook_deliveries deliveries join installations on installations.id=deliveries.installation_id
			where installations.github_id=$1 and cardinality($2::bigint[])>=0 group by deliveries.state`: r.Deliveries,
	} {
		rows, err := s.pool.Query(ctx, query, installationID, repositoryIDs)
		if err != nil {
			return admin.Overview{}, err
		}
		for rows.Next() {
			var state string
			var count int64
			if err := rows.Scan(&state, &count); err != nil {
				rows.Close()
				return admin.Overview{}, err
			}
			target[state] = count
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return admin.Overview{}, err
		}
		rows.Close()
	}
	err := s.pool.QueryRow(ctx, `select
		(select count(*) from scip_uploads uploads join repositories on repositories.id=uploads.repository_id
		 join installations on installations.id=repositories.installation_id where installations.github_id=$1 and repositories.github_id=any($2)),
		(select count(*) from repository_packages packages join repositories on repositories.id=packages.repository_id
		 join installations on installations.id=repositories.installation_id where installations.github_id=$1 and repositories.github_id=any($2)),
		(select count(*) from installations where github_id=$1),
		(select count(*) from search_nodes where exists(select 1 from repositories join installations on installations.id=repositories.installation_id
		 where installations.github_id=$1 and repositories.github_id=any($2)))`, installationID, repositoryIDs).
		Scan(&r.SCIPUploads, &r.Dependencies, &r.Installations, &r.SearchNodes)
	return r, err
}

func (s *Store) AdminRepositories(ctx context.Context, installationID int64, repositoryIDs []int64, limit int) ([]admin.Repository, bool, error) {
	rows, err := s.pool.Query(ctx, `select repositories.id,repositories.github_id,installations.github_id,
		repositories.owner||'/'||repositories.name,repositories.default_branch,coalesce(repositories.desired_sha,''),
		coalesce(repositories.indexed_sha,''),repositories.status,coalesce(repositories.error_code,''),
		repositories.web_url,repositories.enabled,repositories.private,repositories.archived,repositories.last_indexed_at
		from repositories join installations on installations.id=repositories.installation_id
		where installations.github_id=$1 and repositories.github_id=any($2)
		order by repositories.owner,repositories.name limit $3`, installationID, repositoryIDs, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]admin.Repository, 0, limit+1)
	for rows.Next() {
		var x admin.Repository
		if err := rows.Scan(&x.ID, &x.GitHubID, &x.InstallationID, &x.Name, &x.DefaultBranch, &x.DesiredSHA,
			&x.IndexedSHA, &x.Status, &x.ErrorCode, &x.WebURL, &x.Enabled, &x.Private, &x.Archived, &x.LastIndexedAt); err != nil {
			return nil, false, err
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

func (s *Store) AdminRepository(ctx context.Context, installationID int64, repositoryIDs []int64, githubID int64) (repository.Repository, error) {
	return s.AuthorizedRepository(ctx, installationID, repositoryIDs, githubID)
}

func (s *Store) AdminGitHub(ctx context.Context, installationID int64, _ []int64, c admin.GitHubConfig, limit int) (admin.GitHub, error) {
	r := admin.GitHub{AppID: c.AppID, WebURL: c.WebURL, APIURL: c.APIURL, UploadURL: c.UploadURL, GitURL: c.GitURL, APIVersion: c.APIVersion,
		PrivateKeyConfigured: c.PrivateKeyConfigured, WebhookSecretConfigured: c.WebhookSecretConfigured, CAConfigured: c.CAConfigured, Installations: []admin.Installation{}}
	rows, err := s.pool.Query(ctx, `select github_id,account_login,account_type,status,suspended_at
		from installations where github_id=$1 order by github_id limit $2`, installationID, limit+1)
	if err != nil {
		return admin.GitHub{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var x admin.Installation
		if err := rows.Scan(&x.GitHubID, &x.AccountLogin, &x.AccountType, &x.Status, &x.SuspendedAt); err != nil {
			return admin.GitHub{}, err
		}
		r.Installations = append(r.Installations, x)
	}
	if err := rows.Err(); err != nil {
		return admin.GitHub{}, err
	}
	r.Truncated = len(r.Installations) > limit
	if r.Truncated {
		r.Installations = r.Installations[:limit]
	}
	return r, nil
}

func (s *Store) ReconcileAdminRepositories(ctx context.Context, installationID int64, repositoryIDs []int64, upstream []githubapp.Repository) error {
	allowed := make(map[int64]struct{}, len(repositoryIDs))
	for _, id := range repositoryIDs {
		allowed[id] = struct{}{}
	}
	found := make([]int64, 0, len(upstream))
	for _, repo := range upstream {
		if repo.InstallationID != installationID {
			return pgx.ErrNoRows
		}
		if _, ok := allowed[repo.ID]; !ok {
			return pgx.ErrNoRows
		}
		stored, err := s.UpsertRepository(ctx, RepositoryUpdate{
			GitHubID: repo.ID, InstallationID: installationID, Owner: repo.Owner, Name: repo.Name,
			CloneURL: repo.CloneURL, WebURL: repo.HTMLURL, DefaultBranch: repo.DefaultBranch,
			SizeBytes: repo.SizeBytes, Private: repo.Private, Archived: repo.Archived,
			Enabled: !repo.Disabled && !repo.Archived,
		})
		if err != nil {
			return err
		}
		found = append(found, repo.ID)
		if !repo.Disabled && !repo.Archived {
			if err := s.EnqueueIndex(ctx, IndexRequest{
				RepositoryID: stored.ID, TargetSHA: repo.DefaultSHA,
				TargetRef: "refs/heads/" + repo.DefaultBranch, Reason: "admin_reconcile",
			}); err != nil {
				return err
			}
		}
	}
	_, err := s.pool.Exec(ctx, `update repositories set enabled=false,status='disabled',
		error_code='repository_unavailable',updated_at=now() where installation_id=
		(select id from installations where github_id=$1) and github_id=any($2) and not github_id=any($3)`,
		installationID, repositoryIDs, found)
	return err
}

type InstallationUpdate struct {
	GitHubID                          int64
	AccountLogin, AccountType, Status string
	SuspendedAt                       *time.Time
}

type RepositoryUpdate struct {
	GitHubID, InstallationID      int64
	SizeBytes                     int64
	Owner, Name, CloneURL, WebURL string
	DefaultBranch                 string
	Private, Archived, Enabled    bool
}

func (s *Store) UpsertInstallation(ctx context.Context, update InstallationUpdate) error {
	_, err := s.pool.Exec(ctx, `
		insert into installations (github_id, account_login, account_type, status, suspended_at)
		values ($1, $2, $3, $4, $5)
		on conflict (github_id) do update set
			account_login = excluded.account_login, account_type = excluded.account_type,
			status = excluded.status, suspended_at = excluded.suspended_at, updated_at = now()`,
		update.GitHubID, update.AccountLogin, update.AccountType, update.Status, update.SuspendedAt)
	return err
}

func (s *Store) UpsertRepository(ctx context.Context, update RepositoryUpdate) (repository.Repository, error) {
	row := s.pool.QueryRow(ctx, `
		insert into repositories (github_id, installation_id, owner, name, clone_url, web_url, size_bytes, default_branch, private, archived, enabled, status)
		select $1, installations.id, $3, $4, $5, $6, $7, $8, $9, $10, $11,
			case when $11 then 'pending' else 'disabled' end
		from installations where installations.github_id = $2
		on conflict (github_id) do update set
			installation_id = excluded.installation_id, owner = excluded.owner, name = excluded.name,
			clone_url = excluded.clone_url, web_url = excluded.web_url, size_bytes = excluded.size_bytes, default_branch = excluded.default_branch,
			private = excluded.private, archived = excluded.archived, enabled = excluded.enabled,
			status = case when excluded.enabled then repositories.status else 'disabled' end, updated_at = now()
		returning id, (select github_id from installations where id = repositories.installation_id), github_id,
			zoekt_repo_id, owner || '/' || name, size_bytes, default_branch, coalesce(desired_sha, ''), coalesce(indexed_sha, ''),
			web_url, status, coalesce(error_code, ''), coalesce((select node_id from search_nodes where singleton), ''),
			enabled, last_indexed_at`,
		update.GitHubID, update.InstallationID, update.Owner, update.Name, update.CloneURL, update.WebURL, update.SizeBytes,
		update.DefaultBranch, update.Private, update.Archived, update.Enabled)
	return scanRepository(row)
}

func (s *Store) InstallationIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, "select github_id from installations order by github_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) ReconcileInstallation(ctx context.Context, installation githubapp.Installation, repositories []githubapp.Repository) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		insert into installations (github_id, account_login, account_type, status, suspended_at)
		values ($1, $2, $3, $4, $5)
		on conflict (github_id) do update set account_login=excluded.account_login,
			account_type=excluded.account_type, status=excluded.status,
			suspended_at=excluded.suspended_at, updated_at=now()`, installation.ID,
		installation.AccountLogin, installation.AccountType, installation.Status, installation.SuspendedAt); err != nil {
		return err
	}
	ids := make([]int64, len(repositories))
	paths := make([]string, len(repositories))
	for index, repository := range repositories {
		ids[index] = repository.ID
		paths[index] = repository.Owner + "/" + repository.Name
	}
	if _, err := tx.Exec(ctx, `update repositories set owner='', name=github_id::text
		where github_id=any($2) or (installation_id=(select id from installations where github_id=$1)
			and owner || '/' || name=any($3))`, installation.ID, ids, paths); err != nil {
		return err
	}
	for _, repository := range repositories {
		enabled := installation.Status == "active" && installation.SuspendedAt == nil && !repository.Archived && !repository.Disabled
		var id int64
		var desiredSHA *string
		var status string
		if err := tx.QueryRow(ctx, `
			insert into repositories (github_id, installation_id, owner, name, clone_url, web_url, size_bytes,
				default_branch, private, archived, enabled, status)
			select $1, id, $3, $4, $5, $6, $7, $8, $9, $10, $11,
				case when $11 then 'pending' else 'disabled' end
			from installations where github_id=$2
			on conflict (github_id) do update set installation_id=excluded.installation_id,
				owner=excluded.owner, name=excluded.name, clone_url=excluded.clone_url,
				web_url=excluded.web_url, size_bytes=excluded.size_bytes, default_branch=excluded.default_branch,
				private=excluded.private, archived=excluded.archived, enabled=excluded.enabled,
				status=case when not excluded.enabled then 'disabled'
					when repositories.status='disabled' and repositories.indexed_sha=repositories.desired_sha then 'ready'
					when repositories.status='disabled' then 'pending' else repositories.status end,
				error_code=case when excluded.enabled and repositories.status='disabled' then null
					when not excluded.enabled then null else repositories.error_code end,
				updated_at=now()
			returning id, desired_sha, status`, repository.ID, installation.ID, repository.Owner,
			repository.Name, repository.CloneURL, repository.HTMLURL, repository.SizeBytes, repository.DefaultBranch,
			repository.Private, repository.Archived, enabled).Scan(&id, &desiredSHA, &status); err != nil {
			return err
		}
		if enabled && (desiredSHA == nil || *desiredSHA != repository.DefaultSHA || status == "pending") {
			if err := enqueueIndex(ctx, tx, IndexRequest{RepositoryID: id, TargetSHA: repository.DefaultSHA}); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `update repositories set enabled=false, status='disabled', error_code='missing', updated_at=now()
		where installation_id=(select id from installations where github_id=$1) and not (github_id=any($2))`, installation.ID, ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update index_jobs set state='superseded', error_code='repository_unavailable',
		error_message=null, updated_at=now() from repositories join installations on installations.id=repositories.installation_id
		where index_jobs.repository_id=repositories.id and index_jobs.state='queued'
		and installations.github_id=$1 and (not repositories.enabled or repositories.archived or installations.status<>'active')`, installation.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DisableInstallation(ctx context.Context, githubID int64, status string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `update installations set status=$2::varchar, suspended_at=case when $2::varchar='suspended' then now() else suspended_at end, updated_at=now() where github_id=$1`, githubID, status); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update repositories set enabled=false, status='disabled', updated_at=now()
		where installation_id=(select id from installations where github_id=$1)`, githubID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update index_jobs set state='superseded', error_code='repository_unavailable',
		error_message=null, updated_at=now() where state='queued' and repository_id in
		(select id from repositories where installation_id=(select id from installations where github_id=$1))`, githubID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) AuthorizedRepositories(ctx context.Context, installationID int64, repositoryIDs []int64, names []string) ([]repository.Repository, error) {
	if len(repositoryIDs) == 0 {
		return []repository.Repository{}, nil
	}
	rows, err := s.pool.Query(ctx, repositoryQuery, installationID, repositoryIDs, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repositories []repository.Repository
	for rows.Next() {
		repository, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func (s *Store) AuthorizedRepository(ctx context.Context, installationID int64, repositoryIDs []int64, repositoryID int64) (repository.Repository, error) {
	return scanRepository(s.pool.QueryRow(ctx, repositoryQuery+" and repositories.github_id = $4", installationID, repositoryIDs, []string{}, repositoryID))
}

func (s *Store) AllAuthorizedRepositories(ctx context.Context, names []string) ([]repository.Repository, error) {
	rows, err := s.pool.Query(ctx, allRepositoriesQuery+" order by repositories.owner, repositories.name", names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repositories []repository.Repository
	for rows.Next() {
		repository, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func (s *Store) AnyAuthorizedRepository(ctx context.Context, repositoryID int64) (repository.Repository, error) {
	return scanRepository(s.pool.QueryRow(ctx, allRepositoriesQuery+" and repositories.github_id = $2", []string{}, repositoryID))
}

func (s *Store) RepositoryForIndex(ctx context.Context, id int64) (repository.Repository, error) {
	return scanRepository(s.pool.QueryRow(ctx, repositoryByIDQuery, id))
}

func (s *Store) DesiredSHA(ctx context.Context, id int64) (string, error) {
	var desiredSHA string
	err := s.pool.QueryRow(ctx, "select coalesce(desired_sha, '') from repositories where id = $1", id).Scan(&desiredSHA)
	return desiredSHA, err
}

func (s *Store) UpsertSearchNode(ctx context.Context, nodeID, baseURL string) error {
	_, err := s.pool.Exec(ctx, `
		insert into search_nodes (singleton, node_id, base_url, state, capacity_weight)
		values (true, $1, $2, 'active', 1)
		on conflict (singleton) do update set node_id = excluded.node_id, base_url = excluded.base_url,
			state = excluded.state, capacity_weight = excluded.capacity_weight, updated_at = now()`, nodeID, baseURL)
	return err
}

const repositoryColumns = `repositories.id, installations.github_id, repositories.github_id, repositories.zoekt_repo_id,
	repositories.owner || '/' || repositories.name, repositories.size_bytes, repositories.default_branch, coalesce(repositories.desired_sha, ''),
	coalesce(repositories.indexed_sha, ''), repositories.web_url, repositories.status, coalesce(repositories.error_code, ''),
	coalesce((select node_id from search_nodes where singleton), ''), repositories.enabled, repositories.last_indexed_at`

const repositoryQuery = `select ` + repositoryColumns + ` from repositories join installations on installations.id = repositories.installation_id
	where installations.github_id = $1 and repositories.github_id = any($2) and installations.status = 'active'
	and repositories.enabled and not repositories.archived and (coalesce(cardinality($3::text[]), 0) = 0 or repositories.owner || '/' || repositories.name = any($3))`

const allRepositoriesQuery = `select ` + repositoryColumns + ` from repositories join installations on installations.id = repositories.installation_id
	where installations.status = 'active' and repositories.enabled and not repositories.archived
	and (coalesce(cardinality($1::text[]), 0) = 0 or repositories.owner || '/' || repositories.name = any($1))`

const repositoryByIDQuery = `select ` + repositoryColumns + ` from repositories join installations on installations.id = repositories.installation_id where repositories.id = $1`

type repositoryScanner interface{ Scan(...any) error }

func scanRepository(row repositoryScanner) (repository.Repository, error) {
	var result repository.Repository
	var zoektID int64
	err := row.Scan(&result.ID, &result.InstallationID, &result.GitHubID, &zoektID, &result.Name, &result.SizeBytes, &result.Branch,
		&result.DesiredSHA, &result.IndexedSHA, &result.WebURL, &result.Status, &result.ErrorCode, &result.SearchNode, &result.Enabled, &result.LastIndexedAt)
	result.ZoektID = uint32(zoektID)
	return result, err
}
