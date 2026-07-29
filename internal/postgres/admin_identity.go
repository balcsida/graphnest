package postgres

import (
	"context"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/jackc/pgx/v5"
)

const adminIdentityLock int64 = 0x677265706e657374

func (s *Store) AdminUsers(ctx context.Context, limit int) ([]admin.User, bool, error) {
	rows, err := s.pool.Query(ctx, adminUsersSQL+` order by users.id limit $1`, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	users := make([]admin.User, 0, limit)
	for rows.Next() {
		user, err := scanAdminUser(rows)
		if err != nil {
			return nil, false, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(users) > limit
	if truncated {
		users = users[:limit]
	}
	return users, truncated, nil
}

func (s *Store) AdminUser(ctx context.Context, id int64) (admin.User, error) {
	return scanAdminUser(s.pool.QueryRow(ctx, adminUsersSQL+` and users.id=$1`, id))
}

const adminUsersSQL = `select users.id, users.external_id, users.user_name, users.display_name,
	users.source, users.scim_active, users.suspended_at is not null,
	users.scim_active and users.suspended_at is null and (
		exists(select 1 from user_roles where user_roles.user_id=users.id)
			or exists(select 1 from group_memberships
				join groups on groups.id=group_memberships.group_id and groups.deleted_at is null
				join group_roles on group_roles.group_id=groups.id
				where group_memberships.user_id=users.id)),
	case when users.scim_active and users.suspended_at is null then coalesce(array(
		select grants.repository_id from (
			select repository_id from user_repository_grants where user_id=users.id
			union
			select group_grants.repository_id from group_memberships
				join groups on groups.id=group_memberships.group_id and groups.deleted_at is null
				join group_repository_grants group_grants on group_grants.group_id=groups.id
				where group_memberships.user_id=users.id
		) grants
		join repositories on repositories.github_id=grants.repository_id
		join installations on installations.id=repositories.installation_id
		where installations.status='active' and repositories.enabled and not repositories.archived
		order by grants.repository_id
	), '{}') else '{}' end
	from users where users.deleted_at is null`

type rowScanner interface{ Scan(...any) error }

func scanAdminUser(row rowScanner) (admin.User, error) {
	var user admin.User
	err := row.Scan(&user.ID, &user.ExternalID, &user.UserName, &user.DisplayName,
		&user.Source, &user.SCIMActive, &user.Suspended, &user.Administrator, &user.RepositoryIDs)
	return user, err
}

func (s *Store) AdminGroups(ctx context.Context, limit int) ([]admin.Group, bool, error) {
	rows, err := s.pool.Query(ctx, adminGroupsSQL+` order by groups.id limit $1`, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	groups := make([]admin.Group, 0, limit)
	for rows.Next() {
		group, err := scanAdminGroup(rows)
		if err != nil {
			return nil, false, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(groups) > limit
	if truncated {
		groups = groups[:limit]
	}
	return groups, truncated, nil
}

func (s *Store) AdminGroup(ctx context.Context, id int64) (admin.Group, error) {
	return scanAdminGroup(s.pool.QueryRow(ctx, adminGroupsSQL+` and groups.id=$1`, id))
}

const adminGroupsSQL = `select groups.id, groups.external_id, groups.display_name,
	exists(select 1 from group_roles where group_roles.group_id=groups.id),
	coalesce(array(
		select grants.repository_id from group_repository_grants grants
		join repositories on repositories.github_id=grants.repository_id
		join installations on installations.id=repositories.installation_id
		where grants.group_id=groups.id and installations.status='active'
			and repositories.enabled and not repositories.archived
		order by grants.repository_id
	), '{}'),
	(select count(*) from group_memberships where group_id=groups.id)
	from groups where groups.deleted_at is null`

func scanAdminGroup(row rowScanner) (admin.Group, error) {
	var group admin.Group
	err := row.Scan(&group.ID, &group.ExternalID, &group.DisplayName,
		&group.Administrator, &group.RepositoryIDs, &group.MemberCount)
	return group, err
}

func (s *Store) SuspendAdminUser(ctx context.Context, actorID, userID int64, suspended bool) error {
	return s.adminIdentityMutation(ctx, func(tx pgx.Tx) error {
		if actorID == userID && suspended {
			return admin.ErrSelfAdministration
		}
		tag, err := tx.Exec(ctx, `update users set suspended_at=case when $2 then now() else null end, updated_at=now()
			where id=$1 and deleted_at is null`, userID, suspended)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if suspended {
			if err := revokeAdminCredentials(ctx, tx, userID); err != nil {
				return err
			}
			return protectAdministrators(ctx, tx, actorID)
		}
		return nil
	})
}

func (s *Store) ReplaceAdminUserAccess(ctx context.Context, actorID, userID int64, administrator bool, repositoryIDs []int64) error {
	return s.adminIdentityMutation(ctx, func(tx pgx.Tx) error {
		if actorID == userID && !administrator {
			return admin.ErrSelfAdministration
		}
		if err := requireAdminUser(ctx, tx, userID); err != nil {
			return err
		}
		if err := validateAdminRepositories(ctx, tx, repositoryIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from user_roles where user_id=$1`, userID); err != nil {
			return err
		}
		if administrator {
			if _, err := tx.Exec(ctx, `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `delete from user_repository_grants where user_id=$1`, userID); err != nil {
			return err
		}
		if len(repositoryIDs) > 0 {
			if _, err := tx.Exec(ctx, `insert into user_repository_grants (user_id, repository_id)
				select $1, unnest($2::bigint[])`, userID, repositoryIDs); err != nil {
				return err
			}
		}
		return protectAdministrators(ctx, tx, actorID)
	})
}

func (s *Store) ReplaceAdminGroupAccess(ctx context.Context, actorID, groupID int64, administrator bool, repositoryIDs []int64) error {
	return s.adminIdentityMutation(ctx, func(tx pgx.Tx) error {
		if err := requireAdminGroup(ctx, tx, groupID); err != nil {
			return err
		}
		if err := validateAdminRepositories(ctx, tx, repositoryIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from group_roles where group_id=$1`, groupID); err != nil {
			return err
		}
		if administrator {
			if _, err := tx.Exec(ctx, `insert into group_roles (group_id, administrator) values ($1, true)`, groupID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `delete from group_repository_grants where group_id=$1`, groupID); err != nil {
			return err
		}
		if len(repositoryIDs) > 0 {
			if _, err := tx.Exec(ctx, `insert into group_repository_grants (group_id, repository_id)
				select $1, unnest($2::bigint[])`, groupID, repositoryIDs); err != nil {
				return err
			}
		}
		return protectAdministrators(ctx, tx, actorID)
	})
}

func (s *Store) RevokeAdminUserCredentials(ctx context.Context, userID int64) error {
	return s.adminIdentityMutation(ctx, func(tx pgx.Tx) error {
		if err := requireAdminUser(ctx, tx, userID); err != nil {
			return err
		}
		return revokeAdminCredentials(ctx, tx, userID)
	})
}

func (s *Store) adminIdentityMutation(ctx context.Context, change func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, adminIdentityLock); err != nil {
		return err
	}
	if err := change(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func requireAdminUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	var found int
	return tx.QueryRow(ctx, `select 1 from users where id=$1 and deleted_at is null for update`, userID).Scan(&found)
}

func requireAdminGroup(ctx context.Context, tx pgx.Tx, groupID int64) error {
	var found int
	return tx.QueryRow(ctx, `select 1 from groups where id=$1 and deleted_at is null for update`, groupID).Scan(&found)
}

func validateAdminRepositories(ctx context.Context, tx pgx.Tx, repositoryIDs []int64) error {
	if len(repositoryIDs) == 0 {
		return nil
	}
	var count int
	err := tx.QueryRow(ctx, `select count(*) from repositories
		join installations on installations.id=repositories.installation_id
		where repositories.github_id=any($1) and installations.status='active'
			and repositories.enabled and not repositories.archived`, repositoryIDs).Scan(&count)
	if err != nil {
		return err
	}
	if count != len(repositoryIDs) {
		return admin.ErrInvalid
	}
	return nil
}

func protectAdministrators(ctx context.Context, tx pgx.Tx, actorID int64) error {
	if actorID > 0 {
		var actorAdministrator bool
		if err := tx.QueryRow(ctx, `select exists(`+activeAdministratorSQL+` and users.id=$1)`, actorID).Scan(&actorAdministrator); err != nil {
			return err
		}
		if !actorAdministrator {
			return admin.ErrSelfAdministration
		}
	}
	var administratorExists bool
	if err := tx.QueryRow(ctx, `select exists(`+activeAdministratorSQL+`)`).Scan(&administratorExists); err != nil {
		return err
	}
	if !administratorExists {
		return admin.ErrFinalAdministrator
	}
	return nil
}

const activeAdministratorSQL = `select 1 from users
	where users.scim_active and users.suspended_at is null and users.deleted_at is null
		and (exists(select 1 from user_roles where user_roles.user_id=users.id)
			or exists(select 1 from group_memberships
				join groups on groups.id=group_memberships.group_id and groups.deleted_at is null
				join group_roles on group_roles.group_id=groups.id
				where group_memberships.user_id=users.id))`

func revokeAdminCredentials(ctx context.Context, tx pgx.Tx, userID int64) error {
	if _, err := tx.Exec(ctx, `update auth_sessions set revoked_at=coalesce(revoked_at, now())
		where user_id=$1 and revoked_at is null`, userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `update api_tokens set revoked_at=coalesce(revoked_at, now())
		where user_id=$1 and revoked_at is null`, userID)
	return err
}
