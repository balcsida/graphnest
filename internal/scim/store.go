package scim

import (
	"context"
	"errors"
)

var (
	ErrUniqueness         = errors.New("scim uniqueness conflict")
	ErrInvalidMember      = errors.New("scim member does not exist")
	ErrNoTarget           = errors.New("scim patch target does not exist")
	ErrFinalAdministrator = errors.New("cannot remove the final active administrator")
)

type Store interface {
	ListUsers(context.Context, Filter, Page) ([]User, int, error)
	User(context.Context, int64) (User, error)
	CreateUser(context.Context, User) (User, error)
	ReplaceUser(context.Context, int64, User) (User, error)
	PatchUser(context.Context, int64, UserMutation) (User, error)
	DeleteUser(context.Context, int64) error
	ListGroups(context.Context, Filter, Page) ([]Group, int, error)
	Group(context.Context, int64) (Group, error)
	CreateGroup(context.Context, Group) (Group, error)
	ReplaceGroup(context.Context, int64, Group) (Group, error)
	PatchGroup(context.Context, int64, GroupMutation) (Group, error)
	DeleteGroup(context.Context, int64) error
}
