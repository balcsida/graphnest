package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/grepnest/grepnest/internal/audit"
	"github.com/grepnest/grepnest/internal/authn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalidSecurityPrincipal = errors.New("invalid security principal")
	ErrInvalidAuditLimit        = errors.New("invalid audit limit")
)

const (
	breakGlassUserLockNamespace int32 = 0x67726570
	loginFailureLimit                 = 5
	loginFailureWindow                = 15 * time.Minute
)

func (s *Store) SetPasswordCredential(ctx context.Context, userID int64, credential authn.PasswordCredential, event audit.Event) error {
	if err := credential.Validate(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := requireLocalAdministrator(ctx, tx, userID); err != nil {
		return err
	}
	if err := setPasswordCredential(ctx, tx, userID, credential); err != nil {
		return err
	}
	if err := revokeAdminCredentials(ctx, tx, userID); err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CreatePasswordSession(ctx context.Context, userID int64, expected authn.PasswordCredential, session authn.SessionRecord) error {
	if expected.Validate() != nil || expected.ForceRotation || !validStandardLocalSession(userID, session) {
		return authn.ErrInvalidIdentity
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := requireLocalAdministrator(ctx, tx, userID); err != nil {
		return err
	}
	var matches bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from password_credentials
		where user_id=$1 and salt=$2 and hash=$3 and memory_kib=$4
		and iterations=$5 and parallelism=$6 and force_rotation=false)`,
		userID, expected.Salt, expected.Hash, expected.MemoryKiB,
		expected.Iterations, expected.Parallelism).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return authn.ErrUnauthenticated
	}
	if err := createSession(ctx, tx, session); err != nil {
		return err
	}
	for _, operation := range []string{audit.OperationLocalLoginSucceeded, audit.OperationSessionCreated} {
		if err := appendAudit(ctx, tx, audit.Event{
			ActorType: "user", ActorID: strconv.FormatInt(userID, 10),
			TargetType: "session", AuthenticationMethod: "local",
			Operation: operation, Outcome: "success",
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RotatePasswordCredential(ctx context.Context, userID int64, expected, replacement authn.PasswordCredential, session authn.SessionRecord, event audit.Event) error {
	if expected.Validate() != nil || replacement.Validate() != nil || !expected.ForceRotation || replacement.ForceRotation {
		return authn.ErrInvalidPasswordCredential
	}
	if !validStandardLocalSession(userID, session) {
		return authn.ErrInvalidIdentity
	}
	if err := event.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := requireLocalAdministrator(ctx, tx, userID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `update password_credentials set
		salt=$2,hash=$3,memory_kib=$4,iterations=$5,parallelism=$6,
		force_rotation=false,updated_at=now()
		where user_id=$1 and salt=$7 and hash=$8 and memory_kib=$9
		and iterations=$10 and parallelism=$11 and force_rotation=true`,
		userID, replacement.Salt, replacement.Hash, replacement.MemoryKiB,
		replacement.Iterations, replacement.Parallelism, expected.Salt, expected.Hash,
		expected.MemoryKiB, expected.Iterations, expected.Parallelism)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return authn.ErrUnauthenticated
	}
	if err := revokeAdminCredentials(ctx, tx, userID); err != nil {
		return err
	}
	if err := createSession(ctx, tx, session); err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, event); err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, audit.Event{
		ActorType: event.ActorType, ActorID: event.ActorID, TargetType: "session",
		AuthenticationMethod: "local", Operation: audit.OperationSessionCreated,
		Outcome: "success", RequestID: event.RequestID,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validStandardLocalSession(userID int64, session authn.SessionRecord) bool {
	return session.UserID == userID && session.Provider == "local" && !session.ForceRotation &&
		!session.CreatedAt.IsZero() && session.LastSeenAt == session.CreatedAt &&
		session.IdleExpiresAt.After(session.CreatedAt) && !session.ExpiresAt.Before(session.IdleExpiresAt)
}

func (s *Store) UpsertBreakGlassAdmin(ctx context.Context, userName string, credential authn.PasswordCredential, event audit.Event) (int64, error) {
	if !validSecurityUserName(userName) {
		return 0, ErrInvalidSecurityPrincipal
	}
	if err := credential.Validate(); err != nil {
		return 0, err
	}
	if err := event.Validate(); err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1,hashtext(lower($2)))`,
		breakGlassUserLockNamespace, userName); err != nil {
		return 0, err
	}
	var userID int64
	err = tx.QueryRow(ctx, `select id from users where lower(user_name)=lower($1) and deleted_at is null for update`, userName).Scan(&userID)
	switch {
	case err == nil:
		if err := requireLocalAdministrator(ctx, tx, userID); err != nil {
			return 0, err
		}
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `insert into users (external_id,user_name,source)
			values ($1,$1,'local') returning id`, userName).Scan(&userID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `insert into user_roles (user_id,administrator) values ($1,true)`, userID); err != nil {
			return 0, err
		}
	default:
		return 0, err
	}
	if err := setPasswordCredential(ctx, tx, userID, credential); err != nil {
		return 0, err
	}
	if err := revokeAdminCredentials(ctx, tx, userID); err != nil {
		return 0, err
	}
	if err := appendAudit(ctx, tx, event); err != nil {
		return 0, err
	}
	return userID, tx.Commit(ctx)
}

func validSecurityUserName(value string) bool {
	if value == "" || len(value) > 320 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func requireLocalAdministrator(ctx context.Context, tx pgx.Tx, userID int64) error {
	var valid bool
	if err := tx.QueryRow(ctx, `select source='local' and scim_active and suspended_at is null
		and deleted_at is null and exists(select 1 from user_roles where user_id=users.id)
		from users where id=$1 for update`, userID).Scan(&valid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidSecurityPrincipal
		}
		return err
	}
	if !valid {
		return ErrInvalidSecurityPrincipal
	}
	return nil
}

func setPasswordCredential(ctx context.Context, tx pgx.Tx, userID int64, credential authn.PasswordCredential) error {
	_, err := tx.Exec(ctx, `insert into password_credentials
		(user_id,salt,hash,memory_kib,iterations,parallelism,force_rotation,updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,now()) on conflict (user_id) do update set
		salt=excluded.salt,hash=excluded.hash,memory_kib=excluded.memory_kib,
		iterations=excluded.iterations,parallelism=excluded.parallelism,
		force_rotation=excluded.force_rotation,updated_at=excluded.updated_at`,
		userID, credential.Salt, credential.Hash, credential.MemoryKiB,
		credential.Iterations, credential.Parallelism, credential.ForceRotation)
	return err
}

func (s *Store) PasswordCredential(ctx context.Context, userName string) (int64, authn.PasswordCredential, error) {
	var userID int64
	var credential authn.PasswordCredential
	err := s.pool.QueryRow(ctx, `select users.id,credentials.salt,credentials.hash,
		credentials.memory_kib,credentials.iterations,credentials.parallelism,credentials.force_rotation
		from users join password_credentials credentials on credentials.user_id=users.id
		where lower(users.user_name)=lower($1) and users.source='local' and users.scim_active
		and users.suspended_at is null and users.deleted_at is null
		and exists(select 1 from user_roles where user_id=users.id)`, userName).
		Scan(&userID, &credential.Salt, &credential.Hash, &credential.MemoryKiB,
			&credential.Iterations, &credential.Parallelism, &credential.ForceRotation)
	return userID, credential, err
}

func (s *Store) ConsumeLoginAttempt(ctx context.Context, key [32]byte, now time.Time) (bool, time.Time, error) {
	return consumeLoginAttempt(ctx, s.pool, key, now)
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func consumeLoginAttempt(ctx context.Context, query rowQuerier, key [32]byte, now time.Time) (bool, time.Time, error) {
	var allowed bool
	var retryAt time.Time
	err := query.QueryRow(ctx, `insert into login_throttles
		(key_hash,failures,window_started_at) values ($1,1,$2)
		on conflict (key_hash) do update set
		failures=case when login_throttles.window_started_at <= $2-$3::interval
			then 1 else least(login_throttles.failures+1,$4+1) end,
		window_started_at=case when login_throttles.window_started_at <= $2-$3::interval then $2 else login_throttles.window_started_at end,
		blocked_until=case
			when login_throttles.window_started_at <= $2-$3::interval then null
			when login_throttles.failures+1 >= $4
				then coalesce(login_throttles.blocked_until,login_throttles.window_started_at+$3::interval)
			else login_throttles.blocked_until end
		returning failures<=$4,coalesce(blocked_until,window_started_at+$3::interval)`,
		key[:], now, loginFailureWindow.String(), loginFailureLimit).Scan(&allowed, &retryAt)
	return allowed, retryAt, err
}

func (s *Store) ClearLoginFailures(ctx context.Context, accountKey, sourceKey [32]byte) error {
	_, err := s.pool.Exec(ctx, `delete from login_throttles where key_hash=$1 or key_hash=$2`, accountKey[:], sourceKey[:])
	return err
}

func (s *Store) AppendAudit(ctx context.Context, event audit.Event) error {
	return appendAudit(ctx, s.pool, event)
}

func (s *Store) Record(ctx context.Context, event audit.Event) error {
	return s.AppendAudit(ctx, event)
}

type auditExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func appendAudit(ctx context.Context, executor auditExecutor, event audit.Event) error {
	event, err := audit.NewEvent(event)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, `insert into audit_events
		(actor_type,actor_id,target_type,target_id,authentication_method,operation,outcome,request_id,created_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		event.ActorType, event.ActorID, event.TargetType, event.TargetID,
		event.AuthenticationMethod, event.Operation, event.Outcome, event.RequestID, event.CreatedAt)
	return err
}

func (s *Store) AuditEvents(ctx context.Context, limit int) ([]audit.Event, bool, error) {
	if limit < 1 || limit > 100 {
		return nil, false, ErrInvalidAuditLimit
	}
	rows, err := s.pool.Query(ctx, `select actor_type,actor_id,target_type,target_id,
		authentication_method,operation,outcome,request_id,created_at
		from audit_events order by created_at desc,id desc limit $1`, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	events := make([]audit.Event, 0, limit)
	for rows.Next() {
		var event audit.Event
		if err := rows.Scan(&event.ActorType, &event.ActorID, &event.TargetType, &event.TargetID,
			&event.AuthenticationMethod, &event.Operation, &event.Outcome, &event.RequestID, &event.CreatedAt); err != nil {
			return nil, false, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(events) > limit
	if more {
		events = events[:limit]
	}
	return events, more, nil
}
