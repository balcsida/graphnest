package admin

import (
	"context"
	"errors"
	"strconv"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
)

var (
	ErrInvalid            = errors.New("invalid admin request")
	ErrSelfAdministration = errors.New("cannot remove your own administrator access")
	ErrFinalAdministrator = errors.New("cannot remove the final active administrator")
)

type User struct {
	ID                  int64   `json:"id"`
	ExternalID          string  `json:"external_id"`
	UserName            string  `json:"user_name"`
	DisplayName         string  `json:"display_name"`
	Source              string  `json:"source"`
	SCIMActive          bool    `json:"scim_active"`
	Suspended           bool    `json:"suspended"`
	Administrator       bool    `json:"administrator"`
	RepositoryIDs       []int64 `json:"repository_ids"`
	DirectAdministrator bool    `json:"direct_administrator"`
	DirectRepositoryIDs []int64 `json:"direct_repository_ids"`
}

type Group struct {
	ID            int64   `json:"id"`
	ExternalID    string  `json:"external_id"`
	DisplayName   string  `json:"display_name"`
	Administrator bool    `json:"administrator"`
	RepositoryIDs []int64 `json:"repository_ids"`
	MemberCount   int     `json:"member_count"`
}

func (service *Service) Users(ctx context.Context, principal authn.Principal) ([]User, bool, error) {
	if err := requireIdentityAdmin(principal); err != nil {
		return nil, false, err
	}
	return service.Store.AdminUsers(ctx, service.limit())
}

func (service *Service) User(ctx context.Context, principal authn.Principal, id int64) (User, error) {
	if err := requireIdentityAdmin(principal); err != nil {
		return User{}, err
	}
	return service.Store.AdminUser(ctx, id)
}

func (service *Service) Groups(ctx context.Context, principal authn.Principal) ([]Group, bool, error) {
	if err := requireIdentityAdmin(principal); err != nil {
		return nil, false, err
	}
	return service.Store.AdminGroups(ctx, service.limit())
}

func (service *Service) Group(ctx context.Context, principal authn.Principal, id int64) (Group, error) {
	if err := requireIdentityAdmin(principal); err != nil {
		return Group{}, err
	}
	return service.Store.AdminGroup(ctx, id)
}

func (service *Service) SuspendUser(ctx context.Context, principal authn.Principal, id int64, suspended bool) error {
	if err := requireIdentityAdmin(principal); err != nil {
		service.recordDenied(ctx, principal, "user", id)
		return err
	}
	actorID := principalUserID(principal)
	if suspended && actorID == id {
		service.recordDenied(ctx, principal, "user", id)
		return ErrSelfAdministration
	}
	operation := audit.OperationUserRestored
	if suspended {
		operation = audit.OperationUserSuspended
	}
	err := service.Store.SuspendAdminUserAudited(ctx, actorID, id, suspended, identityAudit(ctx, principal, "user", id, operation))
	return service.auditFinalAdministrator(ctx, principal, "user", id, err)
}

func (service *Service) ReplaceUserAccess(ctx context.Context, principal authn.Principal, id int64, administrator bool, repositoryIDs []int64) error {
	if err := requireIdentityAdmin(principal); err != nil {
		service.recordDenied(ctx, principal, "user", id)
		return err
	}
	actorID := principalUserID(principal)
	err := service.Store.ReplaceAdminUserAccessAudited(ctx, actorID, id, administrator, repositoryIDs,
		identityAudit(ctx, principal, "user", id, audit.OperationUserRoleChanged))
	return service.auditFinalAdministrator(ctx, principal, "user", id, err)
}

func (service *Service) ReplaceGroupAccess(ctx context.Context, principal authn.Principal, id int64, administrator bool, repositoryIDs []int64) error {
	if err := requireIdentityAdmin(principal); err != nil {
		service.recordDenied(ctx, principal, "group", id)
		return err
	}
	err := service.Store.ReplaceAdminGroupAccessAudited(ctx, principalUserID(principal), id, administrator, repositoryIDs,
		identityAudit(ctx, principal, "group", id, audit.OperationGroupRoleChanged))
	return service.auditFinalAdministrator(ctx, principal, "group", id, err)
}

func (service *Service) RevokeUserCredentials(ctx context.Context, principal authn.Principal, id int64) error {
	if err := requireIdentityAdmin(principal); err != nil {
		service.recordDenied(ctx, principal, "user", id)
		return err
	}
	return service.Store.RevokeAdminUserCredentialsAudited(ctx, id,
		identityAudit(ctx, principal, "user", id, audit.OperationUserCredentialsRevoked))
}

func (service *Service) auditFinalAdministrator(ctx context.Context, principal authn.Principal, targetType string, targetID int64, err error) error {
	if errors.Is(err, ErrFinalAdministrator) {
		service.recordDenied(ctx, principal, targetType, targetID)
	}
	return err
}

func (service *Service) recordDenied(ctx context.Context, principal authn.Principal, targetType string, targetID int64) {
	if service.Audit == nil {
		return
	}
	actorID := ""
	if id := principalUserID(principal); id > 0 {
		actorID = strconv.FormatInt(id, 10)
	}
	method := principal.Method
	if method != "api_token" && !authn.IsInteractiveMethod(method) {
		method = ""
	}
	target := ""
	if targetID > 0 {
		target = strconv.FormatInt(targetID, 10)
	}
	_ = service.Audit.Record(ctx, audit.Event{
		ActorType: "user", ActorID: actorID, TargetType: targetType,
		TargetID: target, AuthenticationMethod: method,
		Operation: audit.OperationAdminMutationDenied, Outcome: "denied",
		RequestID: audit.RequestID(ctx),
	})
}

func (service *Service) RecordDeniedMutation(ctx context.Context, principal authn.Principal) {
	service.recordDenied(ctx, principal, "authentication", 0)
}

type auditedIdentityStore interface {
	SuspendAdminUserAudited(context.Context, int64, int64, bool, audit.Event) error
	ReplaceAdminUserAccessAudited(context.Context, int64, int64, bool, []int64, audit.Event) error
	ReplaceAdminGroupAccessAudited(context.Context, int64, int64, bool, []int64, audit.Event) error
	RevokeAdminUserCredentialsAudited(context.Context, int64, audit.Event) error
}

func identityAudit(ctx context.Context, principal authn.Principal, targetType string, targetID int64, operation string) audit.Event {
	return audit.Event{
		ActorType: "user", ActorID: principal.Subject, TargetType: targetType,
		TargetID: strconv.FormatInt(targetID, 10), AuthenticationMethod: principal.Method,
		Operation: operation, Outcome: "success",
		RequestID: audit.RequestID(ctx),
	}
}

func principalUserID(principal authn.Principal) int64 {
	id, _ := strconv.ParseInt(principal.Subject, 10, 64)
	return id
}

func requireIdentityAdmin(principal authn.Principal) error {
	if err := requireAdmin(principal); err != nil || !authn.IsInteractiveMethod(principal.Method) {
		return ErrForbidden
	}
	return nil
}
