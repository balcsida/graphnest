package admin

import (
	"context"
	"errors"
	"strconv"

	"github.com/grepnest/grepnest/internal/authn"
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
		return err
	}
	actorID := principalUserID(principal)
	if suspended && actorID == id {
		return ErrSelfAdministration
	}
	return service.Store.SuspendAdminUser(ctx, actorID, id, suspended)
}

func (service *Service) ReplaceUserAccess(ctx context.Context, principal authn.Principal, id int64, administrator bool, repositoryIDs []int64) error {
	if err := requireIdentityAdmin(principal); err != nil {
		return err
	}
	actorID := principalUserID(principal)
	return service.Store.ReplaceAdminUserAccess(ctx, actorID, id, administrator, repositoryIDs)
}

func (service *Service) ReplaceGroupAccess(ctx context.Context, principal authn.Principal, id int64, administrator bool, repositoryIDs []int64) error {
	if err := requireIdentityAdmin(principal); err != nil {
		return err
	}
	return service.Store.ReplaceAdminGroupAccess(ctx, principalUserID(principal), id, administrator, repositoryIDs)
}

func (service *Service) RevokeUserCredentials(ctx context.Context, principal authn.Principal, id int64) error {
	if err := requireIdentityAdmin(principal); err != nil {
		return err
	}
	return service.Store.RevokeAdminUserCredentials(ctx, id)
}

func principalUserID(principal authn.Principal) int64 {
	id, _ := strconv.ParseInt(principal.Subject, 10, 64)
	return id
}

func requireIdentityAdmin(principal authn.Principal) error {
	if err := requireAdmin(principal); err != nil || principal.Method == "api_token" {
		return ErrForbidden
	}
	return nil
}
