package scim

import (
	"context"
	"errors"
	"testing"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/jackc/pgx/v5"
)

func TestServiceValidatesBeforeStore(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Service) error
	}{
		{"user name required", func(s *Service) error { _, err := s.CreateUser(t.Context(), User{ExternalID: "oidc-1"}); return err }},
		{"user external ID required", func(s *Service) error { _, err := s.CreateUser(t.Context(), User{UserName: "ada"}); return err }},
		{"user ID read only", func(s *Service) error {
			_, err := s.CreateUser(t.Context(), User{ID: "9", ExternalID: "oidc-1", UserName: "ada"})
			return err
		}},
		{"user meta read only", func(s *Service) error {
			_, err := s.CreateUser(t.Context(), User{ExternalID: "oidc-1", UserName: "ada", Meta: Meta{Location: "https://evil.test"}})
			return err
		}},
		{"noncanonical email", func(s *Service) error {
			_, err := s.CreateUser(t.Context(), User{ExternalID: "oidc-1", UserName: "ada", Emails: []Email{{Value: "Ada <ada@example.test>"}}})
			return err
		}},
		{"multiple primary emails", func(s *Service) error {
			_, err := s.CreateUser(t.Context(), User{ExternalID: "oidc-1", UserName: "ada", Emails: []Email{{Value: "a@example.test", Primary: true}, {Value: "b@example.test", Primary: true}}})
			return err
		}},
		{"oversized name", func(s *Service) error {
			_, err := s.CreateUser(t.Context(), User{ExternalID: "oidc-1", UserName: "ada", Name: Name{Formatted: string(make([]byte, 257))}})
			return err
		}},
		{"noncanonical email type", func(s *Service) error {
			_, err := s.CreateUser(t.Context(), User{ExternalID: "oidc-1", UserName: "ada", Emails: []Email{{Value: "ada@example.test", Type: "WORK"}}})
			return err
		}},
		{"group display name required", func(s *Service) error { _, err := s.CreateGroup(t.Context(), Group{}); return err }},
		{"member ID must be decimal", func(s *Service) error {
			_, err := s.CreateGroup(t.Context(), Group{DisplayName: "Engineering", Members: []Member{{Value: "01"}}})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{}
			err := test.run(&Service{Store: store, BaseURL: "https://grepnest.example/", MaxResults: 100})
			var scimError Error
			if !errors.As(err, &scimError) || scimError.Status != 400 || store.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, store.calls)
			}
		})
	}
}

func TestServiceDefaultsActiveAndBuildsLocationsFromConfiguredOrigin(t *testing.T) {
	store := &fakeStore{
		createUser: func(user User) (User, error) {
			if user.Active == nil || !*user.Active {
				t.Fatalf("active=%v", user.Active)
			}
			user.ID = "42"
			return user, nil
		},
		createGroup: func(group Group) (Group, error) {
			group.ID = "7"
			group.Members = []Member{{Value: "42"}}
			return group, nil
		},
	}
	service := &Service{Store: store, BaseURL: "https://grepnest.example/", MaxResults: 100}
	user, err := service.CreateUser(t.Context(), User{Schemas: []string{UserSchema}, ExternalID: "oidc-1", UserName: "ada"})
	if err != nil || user.Meta.Location != "https://grepnest.example/scim/v2/Users/42" {
		t.Fatalf("user=%#v err=%v", user, err)
	}
	group, err := service.CreateGroup(t.Context(), Group{Schemas: []string{GroupSchema}, DisplayName: "Engineering", Members: []Member{{Value: "42"}}})
	if err != nil || group.Meta.Location != "https://grepnest.example/scim/v2/Groups/7" ||
		group.Members[0].Ref != "https://grepnest.example/scim/v2/Users/42" {
		t.Fatalf("group=%#v err=%v", group, err)
	}
}

func TestServiceRejectsUnsafeBaseURLBeforeStore(t *testing.T) {
	store := &fakeStore{}
	service := &Service{Store: store, BaseURL: "http://grepnest.example/tenant", MaxResults: 100}
	if _, err := service.CreateUser(t.Context(), User{ExternalID: "oidc-1", UserName: "ada"}); err == nil || store.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, store.calls)
	}
}

func TestServiceUsesPatchParserAndValidatesMutation(t *testing.T) {
	store := &fakeStore{}
	service := &Service{Store: store, BaseURL: "https://grepnest.example", MaxResults: 100}
	request := NewPatchRequest([]PatchOperation{{Op: "replace", Path: "userName", Value: []byte(`""`)}})
	_, err := service.PatchUser(t.Context(), 42, request)
	var scimError Error
	if !errors.As(err, &scimError) || scimError.SCIMType != "invalidValue" || store.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, store.calls)
	}
}

func TestServiceRejectsUnknownPatchFieldsBeforeStore(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Service) error
	}{
		{"user name", func(service *Service) error {
			_, err := service.PatchUser(t.Context(), 1, NewPatchRequest([]PatchOperation{{
				Op: "replace", Path: "name", Value: []byte(`{"givenName":"Ada","unknown":true}`),
			}}))
			return err
		}},
		{"group member", func(service *Service) error {
			_, err := service.PatchGroup(t.Context(), 1, NewPatchRequest([]PatchOperation{{
				Op: "replace", Path: "members", Value: []byte(`[{"value":"1","unknown":true}]`),
			}}))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{}
			err := test.run(&Service{Store: store, BaseURL: "https://grepnest.example", MaxResults: 100})
			var scimError Error
			if !errors.As(err, &scimError) || scimError.Status != 400 || store.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, store.calls)
			}
		})
	}
}

func TestServiceRequiresExactRequestSchemaBeforeStore(t *testing.T) {
	for _, operation := range []struct {
		name   string
		schema string
		run    func(*Service, []string) error
	}{
		{"create user", UserSchema, func(s *Service, schemas []string) error {
			_, err := s.CreateUser(t.Context(), User{Schemas: schemas, ExternalID: "oidc-1", UserName: "ada"})
			return err
		}},
		{"replace user", UserSchema, func(s *Service, schemas []string) error {
			_, err := s.ReplaceUser(t.Context(), 1, User{Schemas: schemas, ExternalID: "oidc-1", UserName: "ada"})
			return err
		}},
		{"create group", GroupSchema, func(s *Service, schemas []string) error {
			_, err := s.CreateGroup(t.Context(), Group{Schemas: schemas, DisplayName: "Engineering"})
			return err
		}},
		{"replace group", GroupSchema, func(s *Service, schemas []string) error {
			_, err := s.ReplaceGroup(t.Context(), 1, Group{Schemas: schemas, DisplayName: "Engineering"})
			return err
		}},
		{"patch user", PatchSchema, func(s *Service, schemas []string) error {
			_, err := s.PatchUser(t.Context(), 1, PatchRequest{Schemas: schemas, Operations: []PatchOperation{{Op: "replace", Path: "active", Value: []byte(`true`)}}})
			return err
		}},
		{"patch group", PatchSchema, func(s *Service, schemas []string) error {
			_, err := s.PatchGroup(t.Context(), 1, PatchRequest{Schemas: schemas, Operations: []PatchOperation{{Op: "add", Path: "members", Value: []byte(`[{"value":"1"}]`)}}})
			return err
		}},
	} {
		for _, schemas := range []struct {
			name  string
			value []string
		}{
			{"absent", nil},
			{"wrong", []string{"urn:example:wrong"}},
			{"extra", []string{operation.schema, "urn:example:extra"}},
		} {
			t.Run(operation.name+"/"+schemas.name, func(t *testing.T) {
				store := &fakeStore{}
				service := &Service{Store: store, BaseURL: "https://grepnest.example", MaxResults: 100}
				err := operation.run(service, schemas.value)
				var scimError Error
				if !errors.As(err, &scimError) || scimError.Status != 400 || scimError.SCIMType != "invalidValue" || store.calls != 0 {
					t.Fatalf("err=%v calls=%d", err, store.calls)
				}
			})
		}
	}
}

func TestServiceRejectsNoncanonicalPatchMemberBeforeStore(t *testing.T) {
	for _, operation := range []PatchOperation{
		{Op: "add", Path: "members", Value: []byte(`[{"value":"01"}]`)},
		{Op: "remove", Path: `members[value eq "01"]`},
	} {
		store := &fakeStore{}
		service := &Service{Store: store, BaseURL: "https://grepnest.example", MaxResults: 100}
		_, err := service.PatchGroup(t.Context(), 1, NewPatchRequest([]PatchOperation{operation}))
		var scimError Error
		if !errors.As(err, &scimError) || scimError.Status != 400 || scimError.SCIMType != "invalidValue" || store.calls != 0 {
			t.Fatalf("operation=%#v err=%v calls=%d", operation, err, store.calls)
		}
	}
}

func TestServiceRejectsUnsupportedNameFieldsBeforeStore(t *testing.T) {
	for _, name := range []Name{
		{MiddleName: "Byron"},
		{HonorificPrefix: "Countess"},
		{HonorificSuffix: "PhD"},
	} {
		store := &fakeStore{}
		service := &Service{Store: store, BaseURL: "https://grepnest.example", MaxResults: 100}
		_, err := service.CreateUser(t.Context(), User{Schemas: []string{UserSchema}, ExternalID: "oidc-1", UserName: "ada", Name: name})
		var scimError Error
		if !errors.As(err, &scimError) || scimError.Status != 400 || scimError.SCIMType != "invalidValue" || store.calls != 0 {
			t.Fatalf("name=%#v err=%v calls=%d", name, err, store.calls)
		}
	}
}

func TestServiceMapsStoreErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		storeErr error
		status   int
		scimType string
	}{
		{"not found", pgx.ErrNoRows, 404, ""},
		{"uniqueness", ErrUniqueness, 409, "uniqueness"},
		{"invalid member", ErrInvalidMember, 400, "invalidValue"},
		{"missing patch target", ErrNoTarget, 400, "noTarget"},
		{"final administrator", ErrFinalAdministrator, 409, ""},
		{"internal", errors.New("database password leaked"), 500, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{
				Store:      &fakeStore{userErr: test.storeErr},
				BaseURL:    "https://grepnest.example",
				MaxResults: 100,
			}
			_, err := service.User(t.Context(), 42, Projection{})
			var scimError Error
			if !errors.As(err, &scimError) || scimError.Status != test.status || scimError.SCIMType != test.scimType ||
				scimError.Detail == test.storeErr.Error() {
				t.Fatalf("err=%#v", err)
			}
		})
	}
}

type failingSCIMAudit struct{ events []audit.Event }

func (recorder *failingSCIMAudit) Record(_ context.Context, event audit.Event) error {
	recorder.events = append(recorder.events, event)
	return errors.New("audit unavailable")
}

func TestServiceAuditsFinalAdministratorDenialAndPreservesConflict(t *testing.T) {
	recorder := &failingSCIMAudit{}
	service := &Service{
		Store: &fakeStore{userErr: ErrFinalAdministrator}, Audit: recorder,
		BaseURL: "https://grepnest.example", MaxResults: 100,
	}
	ctx := audit.WithRequestID(t.Context(), "request-42")
	_, err := service.ReplaceGroup(ctx, 9, Group{
		Schemas: []string{GroupSchema}, DisplayName: "Administrators",
	})
	var scimError Error
	if !errors.As(err, &scimError) || scimError.Status != 409 {
		t.Fatalf("error=%#v", err)
	}
	if len(recorder.events) != 1 || recorder.events[0].TargetType != "group" ||
		recorder.events[0].TargetID != "9" || recorder.events[0].RequestID != "request-42" {
		t.Fatalf("events=%#v", recorder.events)
	}
}

func TestServiceProjectsAndBoundsLists(t *testing.T) {
	active := true
	store := &fakeStore{users: []User{{
		ID: "42", ExternalID: "oidc-1", UserName: "ada", DisplayName: "Ada",
		Active: &active, Emails: []Email{{Value: "ada@example.test"}},
	}}, total: 1}
	service := &Service{Store: store, BaseURL: "https://grepnest.example", MaxResults: 10}
	projection, err := ParseProjection(mapValues("attributes", "userName"), ResourceUsers)
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.Users(t.Context(), Filter{}, Page{StartIndex: 1, Count: 99}, projection)
	if err != nil || store.page.Count != 10 || response.Resources[0].UserName != "ada" ||
		response.Resources[0].ExternalID != "" || response.Resources[0].Meta.Location == "" {
		t.Fatalf("response=%#v page=%#v err=%v", response, store.page, err)
	}
}

func mapValues(key, value string) map[string][]string { return map[string][]string{key: {value}} }

type fakeStore struct {
	calls       int
	page        Page
	users       []User
	total       int
	userErr     error
	createUser  func(User) (User, error)
	createGroup func(Group) (Group, error)
}

func (f *fakeStore) ListUsers(_ context.Context, _ Filter, page Page) ([]User, int, error) {
	f.calls++
	f.page = page
	return f.users, f.total, f.userErr
}
func (f *fakeStore) User(context.Context, int64) (User, error) { f.calls++; return User{}, f.userErr }
func (f *fakeStore) CreateUser(_ context.Context, user User) (User, error) {
	f.calls++
	if f.createUser != nil {
		return f.createUser(user)
	}
	return user, f.userErr
}
func (f *fakeStore) ReplaceUser(context.Context, int64, User) (User, error) {
	f.calls++
	return User{}, f.userErr
}
func (f *fakeStore) PatchUser(context.Context, int64, UserMutation) (User, error) {
	f.calls++
	return User{}, f.userErr
}
func (f *fakeStore) DeleteUser(context.Context, int64) error { f.calls++; return f.userErr }
func (f *fakeStore) ListGroups(context.Context, Filter, Page) ([]Group, int, error) {
	f.calls++
	return nil, 0, f.userErr
}
func (f *fakeStore) Group(context.Context, int64) (Group, error) {
	f.calls++
	return Group{}, f.userErr
}
func (f *fakeStore) CreateGroup(_ context.Context, group Group) (Group, error) {
	f.calls++
	if f.createGroup != nil {
		return f.createGroup(group)
	}
	return group, f.userErr
}
func (f *fakeStore) ReplaceGroup(context.Context, int64, Group) (Group, error) {
	f.calls++
	return Group{}, f.userErr
}
func (f *fakeStore) PatchGroup(context.Context, int64, GroupMutation) (Group, error) {
	f.calls++
	return Group{}, f.userErr
}
func (f *fakeStore) DeleteGroup(context.Context, int64) error { f.calls++; return f.userErr }
func (f *fakeStore) CreateUserAudited(ctx context.Context, user User, _ []audit.Event) (User, error) {
	return f.CreateUser(ctx, user)
}
func (f *fakeStore) ReplaceUserAudited(ctx context.Context, id int64, user User, _ []audit.Event) (User, error) {
	return f.ReplaceUser(ctx, id, user)
}
func (f *fakeStore) PatchUserAudited(ctx context.Context, id int64, mutation UserMutation, _ []audit.Event) (User, error) {
	return f.PatchUser(ctx, id, mutation)
}
func (f *fakeStore) DeleteUserAudited(ctx context.Context, id int64, _ []audit.Event) error {
	return f.DeleteUser(ctx, id)
}
func (f *fakeStore) CreateGroupAudited(ctx context.Context, group Group, _ []audit.Event) (Group, error) {
	return f.CreateGroup(ctx, group)
}
func (f *fakeStore) ReplaceGroupAudited(ctx context.Context, id int64, group Group, _ []audit.Event) (Group, error) {
	return f.ReplaceGroup(ctx, id, group)
}
func (f *fakeStore) PatchGroupAudited(ctx context.Context, id int64, mutation GroupMutation, _ []audit.Event) (Group, error) {
	return f.PatchGroup(ctx, id, mutation)
}
func (f *fakeStore) DeleteGroupAudited(ctx context.Context, id int64, _ []audit.Event) error {
	return f.DeleteGroup(ctx, id)
}
