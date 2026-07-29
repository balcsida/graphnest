package scim

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	"github.com/grepnest/grepnest/internal/audit"
	"github.com/jackc/pgx/v5"
)

const (
	maxExternalIDLength  = 1024
	maxUserNameLength    = 320
	maxDisplayNameLength = 256
	maxEmailCount        = 100
	maxEmailLength       = 320
)

type Service struct {
	Store      Store
	BaseURL    string
	MaxResults int
}

func (s *Service) Users(ctx context.Context, filter Filter, page Page, projection Projection) (ListResponse[User], error) {
	if err := s.validate(); err != nil {
		return ListResponse[User]{}, err
	}
	if err := validateList(ResourceUsers, filter, &page, projection, s.maxResults()); err != nil {
		return ListResponse[User]{}, err
	}
	users, total, err := s.Store.ListUsers(ctx, filter, page)
	if err != nil {
		return ListResponse[User]{}, mapServiceError(err)
	}
	for i := range users {
		if err := s.finishUser(&users[i], projection); err != nil {
			return ListResponse[User]{}, err
		}
	}
	return NewListResponse(users, total, page.StartIndex), nil
}

func (s *Service) User(ctx context.Context, id int64, projection Projection) (User, error) {
	if err := s.validate(); err != nil {
		return User{}, err
	}
	if err := validateRead(id, projection, ResourceUsers); err != nil {
		return User{}, err
	}
	user, err := s.Store.User(ctx, id)
	if err != nil {
		return User{}, mapServiceError(err)
	}
	if err := s.finishUser(&user, projection); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Service) CreateUser(ctx context.Context, user User) (User, error) {
	if err := s.validate(); err != nil {
		return User{}, err
	}
	if err := validateUserWrite(&user, true); err != nil {
		return User{}, err
	}
	var created User
	var err error
	created, err = s.Store.CreateUserAudited(ctx, user, scimEvents("user", "", audit.OperationSCIMUserCreated))
	if err != nil {
		return User{}, mapServiceError(err)
	}
	if err := s.finishUser(&created, Projection{}); err != nil {
		return User{}, err
	}
	return created, nil
}

func (s *Service) ReplaceUser(ctx context.Context, id int64, user User) (User, error) {
	if err := s.validate(); err != nil {
		return User{}, err
	}
	if id < 1 {
		return User{}, invalidValue("resource ID must be a positive decimal integer")
	}
	if err := validateUserWrite(&user, true); err != nil {
		return User{}, err
	}
	operations := []string{audit.OperationSCIMUserReplaced}
	if user.Active != nil && !*user.Active {
		operations = append(operations, audit.OperationSCIMUserDeactivated)
	}
	var replaced User
	var err error
	replaced, err = s.Store.ReplaceUserAudited(ctx, id, user, scimEvents("user", fmt.Sprint(id), operations...))
	if err != nil {
		return User{}, mapServiceError(err)
	}
	if err := s.finishUser(&replaced, Projection{}); err != nil {
		return User{}, err
	}
	return replaced, nil
}

func (s *Service) PatchUser(ctx context.Context, id int64, request PatchRequest) (User, error) {
	if err := s.validate(); err != nil {
		return User{}, err
	}
	if id < 1 {
		return User{}, invalidValue("resource ID must be a positive decimal integer")
	}
	if !validSchemas(request.Schemas, PatchSchema) {
		return User{}, invalidValue("schemas must contain exactly the PatchOp schema")
	}
	mutation, err := ParseUserPatch(request)
	if err != nil {
		return User{}, mapServiceError(err)
	}
	if mutation.UserName.Set && !validRequired(mutation.UserName.Value, maxUserNameLength) {
		return User{}, invalidValue("userName is required and must be bounded")
	}
	if mutation.DisplayName.Set && len(mutation.DisplayName.Value) > maxDisplayNameLength {
		return User{}, invalidValue("displayName is too long")
	}
	if mutation.Emails.Set {
		if err := validateEmails(mutation.Emails.Value); err != nil {
			return User{}, err
		}
	}
	if mutation.Name.Set {
		if err := validateName(mutation.Name.Value); err != nil {
			return User{}, err
		}
	}
	operations := []string{audit.OperationSCIMUserPatched}
	if mutation.Active.Set && !mutation.Active.Value {
		operations = append(operations, audit.OperationSCIMUserDeactivated)
	}
	var patched User
	patched, err = s.Store.PatchUserAudited(ctx, id, mutation, scimEvents("user", fmt.Sprint(id), operations...))
	if err != nil {
		return User{}, mapServiceError(err)
	}
	if err := s.finishUser(&patched, Projection{}); err != nil {
		return User{}, err
	}
	return patched, nil
}

func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	if err := s.validate(); err != nil {
		return err
	}
	if id < 1 {
		return invalidValue("resource ID must be a positive decimal integer")
	}
	return mapServiceError(s.Store.DeleteUserAudited(ctx, id, scimEvents("user", fmt.Sprint(id),
		audit.OperationSCIMUserDeactivated, audit.OperationSCIMUserDeleted)))
}

func (s *Service) Groups(ctx context.Context, filter Filter, page Page, projection Projection) (ListResponse[Group], error) {
	if err := s.validate(); err != nil {
		return ListResponse[Group]{}, err
	}
	if err := validateList(ResourceGroups, filter, &page, projection, s.maxResults()); err != nil {
		return ListResponse[Group]{}, err
	}
	groups, total, err := s.Store.ListGroups(ctx, filter, page)
	if err != nil {
		return ListResponse[Group]{}, mapServiceError(err)
	}
	for i := range groups {
		if err := s.finishGroup(&groups[i], projection); err != nil {
			return ListResponse[Group]{}, err
		}
	}
	return NewListResponse(groups, total, page.StartIndex), nil
}

func (s *Service) Group(ctx context.Context, id int64, projection Projection) (Group, error) {
	if err := s.validate(); err != nil {
		return Group{}, err
	}
	if err := validateRead(id, projection, ResourceGroups); err != nil {
		return Group{}, err
	}
	group, err := s.Store.Group(ctx, id)
	if err != nil {
		return Group{}, mapServiceError(err)
	}
	if err := s.finishGroup(&group, projection); err != nil {
		return Group{}, err
	}
	return group, nil
}

func (s *Service) CreateGroup(ctx context.Context, group Group) (Group, error) {
	if err := s.validate(); err != nil {
		return Group{}, err
	}
	if err := validateGroupWrite(group); err != nil {
		return Group{}, err
	}
	operations := []string{audit.OperationSCIMGroupCreated}
	if len(group.Members) > 0 {
		operations = append(operations, audit.OperationGroupMembershipChanged)
	}
	var created Group
	var err error
	created, err = s.Store.CreateGroupAudited(ctx, group, scimEvents("group", "", operations...))
	if err != nil {
		return Group{}, mapServiceError(err)
	}
	if err := s.finishGroup(&created, Projection{}); err != nil {
		return Group{}, err
	}
	return created, nil
}

func (s *Service) ReplaceGroup(ctx context.Context, id int64, group Group) (Group, error) {
	if err := s.validate(); err != nil {
		return Group{}, err
	}
	if id < 1 {
		return Group{}, invalidValue("resource ID must be a positive decimal integer")
	}
	if err := validateGroupWrite(group); err != nil {
		return Group{}, err
	}
	operations := []string{audit.OperationSCIMGroupReplaced, audit.OperationGroupMembershipChanged}
	var replaced Group
	var err error
	replaced, err = s.Store.ReplaceGroupAudited(ctx, id, group, scimEvents("group", fmt.Sprint(id), operations...))
	if err != nil {
		return Group{}, mapServiceError(err)
	}
	if err := s.finishGroup(&replaced, Projection{}); err != nil {
		return Group{}, err
	}
	return replaced, nil
}

func (s *Service) PatchGroup(ctx context.Context, id int64, request PatchRequest) (Group, error) {
	if err := s.validate(); err != nil {
		return Group{}, err
	}
	if id < 1 {
		return Group{}, invalidValue("resource ID must be a positive decimal integer")
	}
	if !validSchemas(request.Schemas, PatchSchema) {
		return Group{}, invalidValue("schemas must contain exactly the PatchOp schema")
	}
	mutation, err := ParseGroupPatch(request)
	if err != nil {
		return Group{}, mapServiceError(err)
	}
	operations := []string{audit.OperationSCIMGroupPatched}
	if mutation.ReplaceMembers != nil || len(mutation.AddMembers) > 0 || len(mutation.RemoveMembers) > 0 {
		operations = append(operations, audit.OperationGroupMembershipChanged)
	}
	var patched Group
	patched, err = s.Store.PatchGroupAudited(ctx, id, mutation, scimEvents("group", fmt.Sprint(id), operations...))
	if err != nil {
		return Group{}, mapServiceError(err)
	}
	if err := s.finishGroup(&patched, Projection{}); err != nil {
		return Group{}, err
	}
	return patched, nil
}

func (s *Service) DeleteGroup(ctx context.Context, id int64) error {
	if err := s.validate(); err != nil {
		return err
	}
	if id < 1 {
		return invalidValue("resource ID must be a positive decimal integer")
	}
	return mapServiceError(s.Store.DeleteGroupAudited(ctx, id, scimEvents("group", fmt.Sprint(id), audit.OperationSCIMGroupDeleted)))
}

type auditedStore interface {
	CreateUserAudited(context.Context, User, []audit.Event) (User, error)
	ReplaceUserAudited(context.Context, int64, User, []audit.Event) (User, error)
	PatchUserAudited(context.Context, int64, UserMutation, []audit.Event) (User, error)
	DeleteUserAudited(context.Context, int64, []audit.Event) error
	CreateGroupAudited(context.Context, Group, []audit.Event) (Group, error)
	ReplaceGroupAudited(context.Context, int64, Group, []audit.Event) (Group, error)
	PatchGroupAudited(context.Context, int64, GroupMutation, []audit.Event) (Group, error)
	DeleteGroupAudited(context.Context, int64, []audit.Event) error
}

func scimEvents(targetType, targetID string, operations ...string) []audit.Event {
	events := make([]audit.Event, len(operations))
	for index, operation := range operations {
		events[index] = audit.Event{
			ActorType: "scim", ActorID: "provisioning", TargetType: targetType, TargetID: targetID,
			AuthenticationMethod: "scim_token", Operation: operation, Outcome: "success",
		}
	}
	return events
}

func validateUserWrite(user *User, defaultActive bool) error {
	if user.ID != "" || user.Meta != (Meta{}) {
		return Error{Status: 400, SCIMType: "mutability", Detail: "id and meta are read-only"}
	}
	if !validSchemas(user.Schemas, UserSchema) {
		return invalidValue("schemas must contain only the core User schema")
	}
	if !validRequired(user.ExternalID, maxExternalIDLength) {
		return invalidValue("externalId is required and must be bounded")
	}
	if !validRequired(user.UserName, maxUserNameLength) {
		return invalidValue("userName is required and must be bounded")
	}
	if len(user.DisplayName) > maxDisplayNameLength {
		return invalidValue("displayName is too long")
	}
	if err := validateName(user.Name); err != nil {
		return err
	}
	if err := validateEmails(user.Emails); err != nil {
		return err
	}
	if defaultActive && user.Active == nil {
		active := true
		user.Active = &active
	}
	user.Schemas = []string{UserSchema}
	return nil
}

func validateGroupWrite(group Group) error {
	if group.ID != "" || group.Meta != (Meta{}) {
		return Error{Status: 400, SCIMType: "mutability", Detail: "id and meta are read-only"}
	}
	if !validSchemas(group.Schemas, GroupSchema) {
		return invalidValue("schemas must contain only the core Group schema")
	}
	if !validRequired(group.DisplayName, maxDisplayNameLength) {
		return invalidValue("displayName is required and must be bounded")
	}
	if len(group.ExternalID) > maxExternalIDLength || (group.ExternalID != "" && strings.TrimSpace(group.ExternalID) != group.ExternalID) {
		return invalidValue("externalId must be bounded and canonical")
	}
	for _, member := range group.Members {
		if member.Ref != "" || member.Display != "" {
			return Error{Status: 400, SCIMType: "mutability", Detail: "member reference and display are read-only"}
		}
		if !canonicalID(member.Value) {
			return invalidValue("member values must be positive decimal user IDs")
		}
	}
	return nil
}

func validateEmails(emails []Email) error {
	if len(emails) > maxEmailCount {
		return invalidValue("too many email values")
	}
	primary := 0
	for _, email := range emails {
		if len(email.Value) == 0 || len(email.Value) > maxEmailLength || strings.TrimSpace(email.Value) != email.Value {
			return invalidValue("email values must be bounded canonical addresses")
		}
		address, err := mail.ParseAddress(email.Value)
		if err != nil || address.Address != email.Value {
			return invalidValue("email values must be bounded canonical addresses")
		}
		if email.Type != "" && email.Type != "work" && email.Type != "home" && email.Type != "other" {
			return invalidValue("email type is unsupported")
		}
		if email.Primary {
			primary++
		}
	}
	if primary > 1 {
		return invalidValue("only one email may be primary")
	}
	return nil
}

func validateName(name Name) error {
	if name.MiddleName != "" || name.HonorificPrefix != "" || name.HonorificSuffix != "" {
		return invalidValue("name contains an unsupported attribute")
	}
	for _, value := range []string{
		name.Formatted, name.FamilyName, name.GivenName,
	} {
		if len(value) > maxDisplayNameLength {
			return invalidValue("name value is too long")
		}
	}
	return nil
}

func validSchemas(schemas []string, required string) bool {
	return len(schemas) == 1 && schemas[0] == required
}

func validRequired(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value
}

func canonicalID(value string) bool {
	_, err := parseCanonicalID(value)
	return err == nil
}

func validateRead(id int64, projection Projection, resource ResourceType) error {
	if id < 1 {
		return invalidValue("resource ID must be a positive decimal integer")
	}
	return validateProjection(projection, resource)
}

func validateList(resource ResourceType, filter Filter, page *Page, projection Projection, max int) error {
	if filter.Attribute != "" {
		canonical, ok := equalityFilterAttribute(resource, filter.Attribute)
		if !ok || canonical != filter.Attribute {
			return Error{Status: 400, SCIMType: "invalidFilter", Detail: "filter is unsupported"}
		}
		if filter.Attribute == "id" && !canonicalID(filter.Value) {
			return Error{Status: 400, SCIMType: "invalidFilter", Detail: "filter value is invalid"}
		}
	}
	if page.StartIndex < 1 || page.Count < 0 {
		return invalidValue("pagination is invalid")
	}
	if page.Count > max {
		page.Count = max
	}
	return validateProjection(projection, resource)
}

func validateProjection(projection Projection, resource ResourceType) error {
	for attribute := range projection.Include {
		if _, ok := filterAttribute(resource, attribute); !ok {
			return Error{Status: 400, SCIMType: "invalidPath", Detail: "projection attribute is unsupported"}
		}
	}
	for attribute := range projection.Exclude {
		if _, ok := filterAttribute(resource, attribute); !ok {
			return Error{Status: 400, SCIMType: "invalidPath", Detail: "projection attribute is unsupported"}
		}
	}
	return nil
}

func (s *Service) finishUser(user *User, projection Projection) error {
	location, err := s.location("Users", user.ID)
	if err != nil {
		return err
	}
	user.Schemas = []string{UserSchema}
	user.Meta.ResourceType, user.Meta.Location = "User", location
	projectUser(user, projection)
	return nil
}

func (s *Service) finishGroup(group *Group, projection Projection) error {
	location, err := s.location("Groups", group.ID)
	if err != nil {
		return err
	}
	group.Schemas = []string{GroupSchema}
	group.Meta.ResourceType, group.Meta.Location = "Group", location
	for i := range group.Members {
		ref, err := s.location("Users", group.Members[i].Value)
		if err != nil {
			return err
		}
		group.Members[i].Ref = ref
	}
	projectGroup(group, projection)
	return nil
}

func (s *Service) location(collection, id string) (string, error) {
	origin, err := url.Parse(s.BaseURL)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
		(origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" || !canonicalID(id) {
		return "", Error{Status: 500, Detail: "SCIM service is misconfigured"}
	}
	origin.Path = fmt.Sprintf("/scim/v2/%s/%s", collection, id)
	return origin.String(), nil
}

func (s *Service) validate() error {
	if s.Store == nil {
		return Error{Status: 500, Detail: "SCIM service is misconfigured"}
	}
	_, err := s.location("Users", "1")
	return err
}

func (s *Service) maxResults() int {
	max := s.MaxResults
	if max < 1 {
		max = 1
	}
	if max > maxDiscoveryResults {
		max = maxDiscoveryResults
	}
	return max
}

func projectUser(user *User, projection Projection) {
	if hidden(projection, "externalId") {
		user.ExternalID = ""
	}
	if hidden(projection, "userName") {
		user.UserName = ""
	}
	if hidden(projection, "displayName") {
		user.DisplayName = ""
	}
	if hidden(projection, "active") {
		user.Active = nil
	}
	if hidden(projection, "name") {
		user.Name = Name{}
	}
	if hidden(projection, "emails") {
		user.Emails = nil
	}
}

func projectGroup(group *Group, projection Projection) {
	if hidden(projection, "externalId") {
		group.ExternalID = ""
	}
	if hidden(projection, "displayName") {
		group.DisplayName = ""
	}
	if hidden(projection, "members") {
		group.Members = nil
	}
}

func hidden(projection Projection, attribute string) bool {
	return projection.Exclude[attribute] || projection.includeOnly && !projection.Include[attribute]
}

func invalidValue(detail string) Error {
	return Error{Status: 400, SCIMType: "invalidValue", Detail: detail}
}

func mapServiceError(err error) error {
	if err == nil {
		return nil
	}
	var parsed parseError
	if errors.As(err, &parsed) {
		return Error{Status: 400, SCIMType: parsed.Error(), Detail: "SCIM request is invalid"}
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Error{Status: 404, Detail: "resource not found"}
	case errors.Is(err, ErrUniqueness):
		return Error{Status: 409, SCIMType: "uniqueness", Detail: "resource conflicts with an existing value"}
	case errors.Is(err, ErrInvalidMember):
		return invalidValue("one or more group members are invalid")
	case errors.Is(err, ErrNoTarget):
		return Error{Status: 400, SCIMType: "noTarget", Detail: "PATCH target does not exist"}
	case errors.Is(err, ErrFinalAdministrator):
		return Error{Status: 409, Detail: "operation would remove the final administrator"}
	default:
		return Error{Status: 500, Detail: "internal server error"}
	}
}

func (e Error) Error() string {
	if e.Detail != "" {
		return e.Detail
	}
	return fmt.Sprintf("SCIM request failed with status %d", e.Status)
}
