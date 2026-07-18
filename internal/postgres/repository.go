package postgres

import (
	"context"
	"time"

	"github.com/grepnest/grepnest/internal/repository"
)

type InstallationUpdate struct {
	GitHubID                          int64
	AccountLogin, AccountType, Status string
	SuspendedAt                       *time.Time
}

type RepositoryUpdate struct {
	GitHubID, InstallationID      int64
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
		insert into repositories (github_id, installation_id, owner, name, clone_url, web_url, default_branch, private, archived, enabled, status)
		select $1, installations.id, $3, $4, $5, $6, $7, $8, $9, $10,
			case when $10 then 'pending' else 'disabled' end
		from installations where installations.github_id = $2
		on conflict (github_id) do update set
			installation_id = excluded.installation_id, owner = excluded.owner, name = excluded.name,
			clone_url = excluded.clone_url, web_url = excluded.web_url, default_branch = excluded.default_branch,
			private = excluded.private, archived = excluded.archived, enabled = excluded.enabled,
			status = case when excluded.enabled then repositories.status else 'disabled' end, updated_at = now()
		returning id, (select github_id from installations where id = repositories.installation_id), github_id,
			zoekt_repo_id, owner || '/' || name, default_branch, coalesce(desired_sha, ''), coalesce(indexed_sha, ''),
			web_url, status, coalesce(error_code, ''), enabled, last_indexed_at`,
		update.GitHubID, update.InstallationID, update.Owner, update.Name, update.CloneURL, update.WebURL,
		update.DefaultBranch, update.Private, update.Archived, update.Enabled)
	return scanRepository(row)
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
	repositories.owner || '/' || repositories.name, repositories.default_branch, coalesce(repositories.desired_sha, ''),
	coalesce(repositories.indexed_sha, ''), repositories.web_url, repositories.status, coalesce(repositories.error_code, ''),
	repositories.enabled, repositories.last_indexed_at`

const repositoryQuery = `select ` + repositoryColumns + ` from repositories join installations on installations.id = repositories.installation_id
	where installations.github_id = $1 and repositories.github_id = any($2) and installations.status = 'active'
	and repositories.enabled and not repositories.archived and (coalesce(cardinality($3::text[]), 0) = 0 or repositories.owner || '/' || repositories.name = any($3))`

const repositoryByIDQuery = `select ` + repositoryColumns + ` from repositories join installations on installations.id = repositories.installation_id where repositories.id = $1`

type repositoryScanner interface{ Scan(...any) error }

func scanRepository(row repositoryScanner) (repository.Repository, error) {
	var result repository.Repository
	var zoektID int64
	err := row.Scan(&result.ID, &result.InstallationID, &result.GitHubID, &zoektID, &result.Name, &result.Branch,
		&result.DesiredSHA, &result.IndexedSHA, &result.WebURL, &result.Status, &result.ErrorCode, &result.Enabled, &result.LastIndexedAt)
	result.ZoektID = uint32(zoektID)
	return result, err
}
