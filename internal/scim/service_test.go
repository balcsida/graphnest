package scim

import (
	"context"
	"errors"
	"testing"

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
