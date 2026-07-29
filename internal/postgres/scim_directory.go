package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/grepnest/grepnest/internal/audit"
	"github.com/grepnest/grepnest/internal/scim"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const scimUsersSQL = `select id, external_id, user_name, display_name, scim_active,
	scim_name, scim_emails, created_at, updated_at from users
	where deleted_at is null and source='scim'`

func (s *Store) ListUsers(ctx context.Context, filter scim.Filter, page scim.Page) ([]scim.User, int, error) {
	query, args := scimUsersSQL, []any{}
	switch filter.Attribute {
	case "":
	case "id":
		id, err := strconv.ParseInt(filter.Value, 10, 64)
		if err != nil {
			return []scim.User{}, 0, nil
		}
		args, query = append(args, id), query+` and id=$1`
	case "userName":
		args, query = append(args, filter.Value), query+` and lower(user_name)=lower($1)`
	case "externalId":
		args, query = append(args, filter.Value), query+` and external_id=$1`
	}
	var total int
	if err := s.pool.QueryRow(ctx, `select count(*) from (`+query+`) users`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, page.Count, page.StartIndex-1)
	rows, err := s.pool.Query(ctx, query+` order by id limit $`+strconv.Itoa(len(args)-1)+` offset $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	users := make([]scim.User, 0, page.Count)
	for rows.Next() {
		user, err := scanSCIMUser(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, user)
	}
	return users, total, rows.Err()
}

func (s *Store) User(ctx context.Context, id int64) (scim.User, error) {
	return scanSCIMUser(s.pool.QueryRow(ctx, scimUsersSQL+` and id=$1`, id))
}

func scanSCIMUser(row rowScanner) (scim.User, error) {
	var user scim.User
	var id int64
	var active bool
	var name, emails []byte
	var created, updated time.Time
	if err := row.Scan(&id, &user.ExternalID, &user.UserName, &user.DisplayName, &active,
		&name, &emails, &created, &updated); err != nil {
		return user, err
	}
	if err := json.Unmarshal(name, &user.Name); err != nil {
		return user, err
	}
	if err := json.Unmarshal(emails, &user.Emails); err != nil {
		return user, err
	}
	user.Schemas, user.ID, user.Active = []string{scim.UserSchema}, strconv.FormatInt(id, 10), &active
	user.Meta = scimMeta("User", created, updated)
	return user, nil
}

func (s *Store) CreateUser(ctx context.Context, user scim.User) (created scim.User, err error) {
	err = s.scimMutation(ctx, func(tx pgx.Tx) error {
		active := user.Active == nil || *user.Active
		name, emails, err := scimProfileJSON(user)
		if err != nil {
			return err
		}
		created, err = scanSCIMUser(tx.QueryRow(ctx, `insert into users
			(external_id, user_name, display_name, scim_active, source, scim_name, scim_emails)
			values ($1,$2,$3,$4,'scim',$5,$6)
			returning id, external_id, user_name, display_name, scim_active, scim_name, scim_emails, created_at, updated_at`,
			user.ExternalID, user.UserName, user.DisplayName, active, name, emails))
		setSCIMAuditTarget(ctx, created.ID)
		return err
	})
	return created, mapSCIMError(err)
}

func (s *Store) ReplaceUser(ctx context.Context, id int64, user scim.User) (updated scim.User, err error) {
	err = s.scimMutation(ctx, func(tx pgx.Tx) error {
		active := user.Active == nil || *user.Active
		name, emails, err := scimProfileJSON(user)
		if err != nil {
			return err
		}
		var wasActive bool
		if err := tx.QueryRow(ctx, `select scim_active from users where id=$1 and deleted_at is null and source='scim' for update`, id).Scan(&wasActive); err != nil {
			return err
		}
		hadAdministrator, err := activeAdministratorExists(ctx, tx)
		if err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `update users set external_id=$2::varchar, user_name=$3::varchar, display_name=$4::varchar,
			scim_active=$5, scim_name=$6, scim_emails=$7,
			updated_at=case when (external_id,user_name,display_name,scim_active,scim_name,scim_emails)
				is distinct from ($2::varchar,$3::varchar,$4::varchar,$5,$6::jsonb,$7::jsonb) then now() else updated_at end
			where id=$1 and deleted_at is null and source='scim'
			returning id, external_id, user_name, display_name, scim_active, scim_name, scim_emails, created_at, updated_at`,
			id, user.ExternalID, user.UserName, user.DisplayName, active, name, emails)
		if updated, err = scanSCIMUser(row); err != nil {
			return err
		}
		if wasActive && !active {
			if err := revokeAdminCredentials(ctx, tx, id); err != nil {
				return err
			}
			return protectSCIMAdministrators(ctx, tx, hadAdministrator)
		}
		return nil
	})
	return updated, mapSCIMError(err)
}

func (s *Store) PatchUser(ctx context.Context, id int64, mutation scim.UserMutation) (updated scim.User, err error) {
	err = s.scimMutation(ctx, func(tx pgx.Tx) error {
		current, err := scanSCIMUser(tx.QueryRow(ctx, scimUsersSQL+` and id=$1 for update`, id))
		if err != nil {
			return err
		}
		hadAdministrator, err := activeAdministratorExists(ctx, tx)
		if err != nil {
			return err
		}
		wasActive := *mustActive(current.Active)
		if mutation.Active.Set {
			current.Active = &mutation.Active.Value
		}
		if mutation.UserName.Set {
			current.UserName = mutation.UserName.Value
		}
		if mutation.DisplayName.Set {
			current.DisplayName = mutation.DisplayName.Value
		}
		if mutation.Name.Set {
			current.Name = mutation.Name.Value
		}
		if mutation.Emails.Set {
			current.Emails = mutation.Emails.Value
		}
		name, emails, err := scimProfileJSON(current)
		if err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `update users set user_name=$2::varchar, display_name=$3::varchar, scim_active=$4,
			scim_name=$5, scim_emails=$6,
			updated_at=case when (user_name,display_name,scim_active,scim_name,scim_emails)
				is distinct from ($2::varchar,$3::varchar,$4,$5::jsonb,$6::jsonb) then now() else updated_at end
			where id=$1 and deleted_at is null and source='scim'
			returning id, external_id, user_name, display_name, scim_active, scim_name, scim_emails, created_at, updated_at`,
			id, current.UserName, current.DisplayName, *current.Active, name, emails)
		if updated, err = scanSCIMUser(row); err != nil {
			return err
		}
		if wasActive && !*updated.Active {
			if err := revokeAdminCredentials(ctx, tx, id); err != nil {
				return err
			}
			return protectSCIMAdministrators(ctx, tx, hadAdministrator)
		}
		return nil
	})
	return updated, mapSCIMError(err)
}

func (s *Store) DeleteUser(ctx context.Context, id int64) (err error) {
	err = s.scimMutation(ctx, func(tx pgx.Tx) error {
		hadAdministrator, err := activeAdministratorExists(ctx, tx)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update users set scim_active=false, deleted_at=now(), updated_at=now()
			where id=$1 and deleted_at is null and source='scim'`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if _, err := tx.Exec(ctx, `delete from group_memberships where user_id=$1`, id); err != nil {
			return err
		}
		if err := revokeAdminCredentials(ctx, tx, id); err != nil {
			return err
		}
		return protectSCIMAdministrators(ctx, tx, hadAdministrator)
	})
	return mapSCIMError(err)
}

func scimProfileJSON(user scim.User) ([]byte, []byte, error) {
	name, err := json.Marshal(user.Name)
	if err != nil {
		return nil, nil, err
	}
	emails := user.Emails
	if emails == nil {
		emails = []scim.Email{}
	}
	encodedEmails, err := json.Marshal(emails)
	return name, encodedEmails, err
}

const scimGroupsSQL = `select groups.id, groups.external_id, groups.display_name,
	groups.created_at, groups.updated_at from groups where groups.deleted_at is null`

func (s *Store) ListGroups(ctx context.Context, filter scim.Filter, page scim.Page) ([]scim.Group, int, error) {
	query, args := scimGroupsSQL, []any{}
	switch filter.Attribute {
	case "":
	case "id":
		id, err := strconv.ParseInt(filter.Value, 10, 64)
		if err != nil {
			return []scim.Group{}, 0, nil
		}
		args, query = append(args, id), query+` and groups.id=$1`
	case "displayName":
		args, query = append(args, filter.Value), query+` and lower(groups.display_name)=lower($1)`
	case "externalId":
		args, query = append(args, filter.Value), query+` and groups.external_id=$1`
	}
	var total int
	if err := s.pool.QueryRow(ctx, `select count(*) from (`+query+`) groups`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, page.Count, page.StartIndex-1)
	rows, err := s.pool.Query(ctx, query+` order by groups.id limit $`+strconv.Itoa(len(args)-1)+` offset $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	groups := make([]scim.Group, 0, page.Count)
	for rows.Next() {
		group, err := scanSCIMGroup(ctx, s.pool, rows)
		if err != nil {
			return nil, 0, err
		}
		groups = append(groups, group)
	}
	return groups, total, rows.Err()
}

type scimGroupQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s *Store) Group(ctx context.Context, id int64) (scim.Group, error) {
	return scanSCIMGroup(ctx, s.pool, s.pool.QueryRow(ctx, scimGroupsSQL+` and groups.id=$1`, id))
}

func scanSCIMGroup(ctx context.Context, queryer scimGroupQuerier, row rowScanner) (scim.Group, error) {
	var group scim.Group
	var id int64
	var created, updated time.Time
	if err := row.Scan(&id, &group.ExternalID, &group.DisplayName, &created, &updated); err != nil {
		return group, err
	}
	rows, err := queryer.Query(ctx, `select users.id, users.user_name from group_memberships
		join users on users.id=group_memberships.user_id and users.deleted_at is null and users.source='scim'
		where group_id=$1 order by users.id`, id)
	if err != nil {
		return group, err
	}
	defer rows.Close()
	for rows.Next() {
		var member scim.Member
		var memberID int64
		if err := rows.Scan(&memberID, &member.Display); err != nil {
			return group, err
		}
		member.Value = strconv.FormatInt(memberID, 10)
		group.Members = append(group.Members, member)
	}
	if err := rows.Err(); err != nil {
		return group, err
	}
	group.Schemas, group.ID = []string{scim.GroupSchema}, strconv.FormatInt(id, 10)
	group.Meta = scimMeta("Group", created, updated)
	return group, nil
}

func (s *Store) CreateGroup(ctx context.Context, group scim.Group) (created scim.Group, err error) {
	err = s.scimMutation(ctx, func(tx pgx.Tx) error {
		ids, err := memberValues(group.Members)
		if err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `insert into groups (external_id, display_name) values ($1,$2) returning id`,
			group.ExternalID, group.DisplayName).Scan(&id); err != nil {
			return err
		}
		if err := replaceSCIMMembers(ctx, tx, id, ids, false); err != nil {
			return err
		}
		created, err = scanSCIMGroup(ctx, tx, tx.QueryRow(ctx, scimGroupsSQL+` and groups.id=$1`, id))
		setSCIMAuditTarget(ctx, created.ID)
		return err
	})
	return created, mapSCIMError(err)
}

func (s *Store) ReplaceGroup(ctx context.Context, id int64, group scim.Group) (updated scim.Group, err error) {
	err = s.scimMutation(ctx, func(tx pgx.Tx) error {
		ids, err := memberValues(group.Members)
		if err != nil {
			return err
		}
		if err := requireAdminGroup(ctx, tx, id); err != nil {
			return err
		}
		hadAdministrator, err := activeAdministratorExists(ctx, tx)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update groups set external_id=$2::varchar, display_name=$3::varchar,
			updated_at=case when (external_id,display_name) is distinct from ($2::varchar,$3::varchar) then now() else updated_at end
			where id=$1 and deleted_at is null`, id, group.ExternalID, group.DisplayName)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if err := replaceSCIMMembers(ctx, tx, id, ids, hadAdministrator); err != nil {
			return err
		}
		updated, err = scanSCIMGroup(ctx, tx, tx.QueryRow(ctx, scimGroupsSQL+` and groups.id=$1`, id))
		return err
	})
	return updated, mapSCIMError(err)
}

func (s *Store) PatchGroup(ctx context.Context, id int64, mutation scim.GroupMutation) (updated scim.Group, err error) {
	err = s.scimMutation(ctx, func(tx pgx.Tx) error {
		if err := requireAdminGroup(ctx, tx, id); err != nil {
			return err
		}
		hadAdministrator, err := activeAdministratorExists(ctx, tx)
		if err != nil {
			return err
		}
		if mutation.ReplaceMembers != nil {
			if err := replaceSCIMMembers(ctx, tx, id, *mutation.ReplaceMembers, false); err != nil {
				return err
			}
		}
		if len(mutation.AddMembers) > 0 || len(mutation.RemoveMembers) > 0 {
			if err := validateSCIMMembers(ctx, tx, mutation.AddMembers); err != nil {
				return err
			}
			if len(mutation.RemoveMembers) > 0 {
				var count int
				if err := tx.QueryRow(ctx, `select count(*) from group_memberships
					join users on users.id=group_memberships.user_id and users.source='scim'
					where group_id=$1 and user_id=any($2)`, id, mutation.RemoveMembers).Scan(&count); err != nil {
					return err
				}
				if count != len(mutation.RemoveMembers) {
					return scim.ErrNoTarget
				}
			}
			tag, err := tx.Exec(ctx, `insert into group_memberships (group_id,user_id)
				select $1, unnest($2::bigint[]) on conflict do nothing`, id, mutation.AddMembers)
			if err != nil {
				return err
			}
			removed, err := tx.Exec(ctx, `delete from group_memberships where group_id=$1 and user_id=any($2)`, id, mutation.RemoveMembers)
			if err != nil {
				return err
			}
			if tag.RowsAffected()+removed.RowsAffected() > 0 {
				if _, err := tx.Exec(ctx, `update groups set updated_at=now() where id=$1`, id); err != nil {
					return err
				}
			}
		}
		if err := protectSCIMAdministrators(ctx, tx, hadAdministrator); err != nil {
			return err
		}
		updated, err = scanSCIMGroup(ctx, tx, tx.QueryRow(ctx, scimGroupsSQL+` and groups.id=$1`, id))
		return err
	})
	return updated, mapSCIMError(err)
}

func (s *Store) DeleteGroup(ctx context.Context, id int64) (err error) {
	err = s.scimMutation(ctx, func(tx pgx.Tx) error {
		if err := requireAdminGroup(ctx, tx, id); err != nil {
			return err
		}
		hadAdministrator, err := activeAdministratorExists(ctx, tx)
		if err != nil {
			return err
		}
		for _, statement := range []string{
			`delete from group_memberships where group_id=$1`,
			`delete from group_repository_grants where group_id=$1`,
			`delete from group_roles where group_id=$1`,
		} {
			if _, err := tx.Exec(ctx, statement, id); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `update groups set deleted_at=now(), updated_at=now() where id=$1 and deleted_at is null`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return protectSCIMAdministrators(ctx, tx, hadAdministrator)
	})
	return mapSCIMError(err)
}

func replaceSCIMMembers(ctx context.Context, tx pgx.Tx, groupID int64, ids []int64, hadAdministrator bool) error {
	ids = uniqueInt64(ids)
	if err := validateSCIMMembers(ctx, tx, ids); err != nil {
		return err
	}
	var changed bool
	if err := tx.QueryRow(ctx, `select coalesce(array_agg(user_id order by user_id), '{}') is distinct from $2::bigint[]
		from group_memberships where group_id=$1`, groupID, ids).Scan(&changed); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if _, err := tx.Exec(ctx, `delete from group_memberships where group_id=$1`, groupID); err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `insert into group_memberships (group_id,user_id) select $1, unnest($2::bigint[])`, groupID, ids); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `update groups set updated_at=now() where id=$1`, groupID); err != nil {
		return err
	}
	return protectSCIMAdministrators(ctx, tx, hadAdministrator)
}

func validateSCIMMembers(ctx context.Context, tx pgx.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, `select count(*) from users where id=any($1) and deleted_at is null and source='scim'`, ids).Scan(&count); err != nil {
		return err
	}
	if count != len(uniqueInt64(ids)) {
		return scim.ErrInvalidMember
	}
	return nil
}

func memberValues(members []scim.Member) ([]int64, error) {
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		id, err := strconv.ParseInt(member.Value, 10, 64)
		if err != nil || id < 1 {
			return nil, scim.ErrInvalidMember
		}
		ids = append(ids, id)
	}
	return uniqueInt64(ids), nil
}

func uniqueInt64(ids []int64) []int64 {
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	slices.Sort(unique)
	return unique
}

func (s *Store) scimMutation(ctx context.Context, change func(pgx.Tx) error) error {
	return s.adminIdentityMutation(ctx, func(tx pgx.Tx) error {
		if err := change(tx); err != nil {
			return err
		}
		state, _ := ctx.Value(scimAuditContextKey{}).(*scimAuditState)
		if state == nil {
			return nil
		}
		for _, event := range state.events {
			if event.TargetID == "" {
				event.TargetID = state.targetID
			}
			if err := appendAudit(ctx, tx, event); err != nil {
				return err
			}
		}
		return nil
	})
}

type scimAuditContextKey struct{}
type scimAuditState struct {
	events   []audit.Event
	targetID string
}

func withSCIMAudit(ctx context.Context, targetID string, events []audit.Event) context.Context {
	return context.WithValue(ctx, scimAuditContextKey{}, &scimAuditState{
		events: append([]audit.Event(nil), events...), targetID: targetID,
	})
}

func setSCIMAuditTarget(ctx context.Context, targetID string) {
	if state, _ := ctx.Value(scimAuditContextKey{}).(*scimAuditState); state != nil {
		state.targetID = targetID
	}
}

func (s *Store) CreateUserAudited(ctx context.Context, user scim.User, events []audit.Event) (scim.User, error) {
	return s.CreateUser(withSCIMAudit(ctx, "", events), user)
}
func (s *Store) ReplaceUserAudited(ctx context.Context, id int64, user scim.User, events []audit.Event) (scim.User, error) {
	return s.ReplaceUser(withSCIMAudit(ctx, strconv.FormatInt(id, 10), events), id, user)
}
func (s *Store) PatchUserAudited(ctx context.Context, id int64, mutation scim.UserMutation, events []audit.Event) (scim.User, error) {
	return s.PatchUser(withSCIMAudit(ctx, strconv.FormatInt(id, 10), events), id, mutation)
}
func (s *Store) DeleteUserAudited(ctx context.Context, id int64, events []audit.Event) error {
	return s.DeleteUser(withSCIMAudit(ctx, strconv.FormatInt(id, 10), events), id)
}
func (s *Store) CreateGroupAudited(ctx context.Context, group scim.Group, events []audit.Event) (scim.Group, error) {
	return s.CreateGroup(withSCIMAudit(ctx, "", events), group)
}
func (s *Store) ReplaceGroupAudited(ctx context.Context, id int64, group scim.Group, events []audit.Event) (scim.Group, error) {
	return s.ReplaceGroup(withSCIMAudit(ctx, strconv.FormatInt(id, 10), events), id, group)
}
func (s *Store) PatchGroupAudited(ctx context.Context, id int64, mutation scim.GroupMutation, events []audit.Event) (scim.Group, error) {
	return s.PatchGroup(withSCIMAudit(ctx, strconv.FormatInt(id, 10), events), id, mutation)
}
func (s *Store) DeleteGroupAudited(ctx context.Context, id int64, events []audit.Event) error {
	return s.DeleteGroup(withSCIMAudit(ctx, strconv.FormatInt(id, 10), events), id)
}

func mapSCIMError(err error) error {
	var pgError *pgconn.PgError
	switch {
	case errors.As(err, &pgError) && pgError.Code == "23505":
		return scim.ErrUniqueness
	case errors.Is(err, admin.ErrFinalAdministrator):
		return scim.ErrFinalAdministrator
	default:
		return err
	}
}

func activeAdministratorExists(ctx context.Context, tx pgx.Tx) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `select exists(`+activeAdministratorSQL+`)`).Scan(&exists)
	return exists, err
}

func protectSCIMAdministrators(ctx context.Context, tx pgx.Tx, hadAdministrator bool) error {
	if !hadAdministrator {
		return nil
	}
	return protectAdministrators(ctx, tx, 0)
}

func scimMeta(resourceType string, created, updated time.Time) scim.Meta {
	return scim.Meta{
		ResourceType: resourceType,
		Created:      created.UTC().Format(time.RFC3339Nano),
		LastModified: updated.UTC().Format(time.RFC3339Nano),
	}
}

func mustActive(active *bool) *bool {
	if active == nil {
		value := true
		return &value
	}
	return active
}
