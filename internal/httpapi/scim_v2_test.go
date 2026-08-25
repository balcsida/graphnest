package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/scim"
)

func TestSCIMV2RequiresAuthenticationOnEveryRoute(t *testing.T) {
	handler := scimTestHandler(t, &scimHTTPStore{})
	for _, path := range []string{
		"/scim/v2",
		"/scim/v2/ServiceProviderConfig", "/scim/v2/ResourceTypes",
		"/scim/v2/Schemas", "/scim/v2/Users", "/scim/v2/Groups",
	} {
		response := scimRequest(handler, http.MethodGet, path, "", "")
		if response.Code != http.StatusUnauthorized ||
			response.Header().Get("WWW-Authenticate") != "Bearer" ||
			response.Header().Get("Content-Type") != "application/scim+json" {
			t.Fatalf("%s status=%d headers=%v", path, response.Code, response.Header())
		}
	}
}

func TestSCIMV2RouteContracts(t *testing.T) {
	handler := scimTestHandler(t, &scimHTTPStore{})
	for _, test := range []struct {
		method, path, body string
		status             int
		allow              string
	}{
		{http.MethodGet, "/scim/v2/ServiceProviderConfig", "", 200, ""},
		{http.MethodGet, "/scim/v2/ResourceTypes/User", "", 200, ""},
		{http.MethodGet, "/scim/v2/Schemas/" + scim.UserSchema, "", 200, ""},
		{http.MethodGet, "/scim/v2/Users", "", 200, ""},
		{http.MethodPost, "/scim/v2/Users", `{"schemas":["` + scim.UserSchema + `"],"externalId":"e","userName":"ada"}`, 201, ""},
		{http.MethodGet, "/scim/v2/Users/1", "", 200, ""},
		{http.MethodPut, "/scim/v2/Users/1", `{"schemas":["` + scim.UserSchema + `"],"externalId":"e","userName":"ada"}`, 200, ""},
		{http.MethodPatch, "/scim/v2/Users/1", `{"schemas":["` + scim.PatchSchema + `"],"Operations":[{"op":"replace","path":"active","value":true}]}`, 200, ""},
		{http.MethodDelete, "/scim/v2/Users/1", "", 204, ""},
		{http.MethodPost, "/scim/v2/Users/1", `{}`, 405, "GET, PUT, PATCH, DELETE"},
		{http.MethodPost, "/scim/v2/ServiceProviderConfig", `{}`, 405, "GET"},
		{http.MethodGet, "/scim/v2/Users/1/nested", "", 404, ""},
		{http.MethodGet, "/scim/v2/Users/", "", 404, ""},
		{http.MethodGet, "/scim/v2/Bulk", "", 404, ""},
		{http.MethodGet, "/scim/v2/Me", "", 404, ""},
		{http.MethodPost, "/scim/v2/.search", `{}`, 404, ""},
	} {
		response := scimRequest(handler, test.method, test.path, test.body, "valid")
		if response.Code != test.status || response.Header().Get("Allow") != test.allow {
			t.Fatalf("%s %s status=%d allow=%q body=%q", test.method, test.path, response.Code, response.Header().Get("Allow"), response.Body.String())
		}
		if test.status != 204 && response.Header().Get("Content-Type") != "application/scim+json" {
			t.Fatalf("%s %s content type=%q", test.method, test.path, response.Header().Get("Content-Type"))
		}
		if test.status == 201 {
			if response.Header().Get("Location") != "https://grepnest.example/scim/v2/Users/1" ||
				!strings.Contains(response.Body.String(), `"userName":"ada"`) {
				t.Fatalf("POST response headers=%v body=%q", response.Header(), response.Body.String())
			}
		}
	}
}

func TestSCIMV2RejectsAmbiguousAndOversizedInput(t *testing.T) {
	handler := scimTestHandler(t, &scimHTTPStore{})
	tooManyOperations := make([]scim.PatchOperation, 101)
	patch, _ := json.Marshal(scim.NewPatchRequest(tooManyOperations))
	for _, test := range []struct {
		method, path, body, contentType string
		status                          int
	}{
		{http.MethodPost, "/scim/v2/Users", `{}`, "application/json", 415},
		{http.MethodPost, "/scim/v2/Users", `{}`, "application/scim+json; charset=utf-8", 415},
		{http.MethodPost, "/scim/v2/Users", `{}` + `{}`, "application/scim+json", 400},
		{http.MethodPost, "/scim/v2/Users", `{"schemas":["` + scim.UserSchema + `"],"externalId":"e","userName":"ada","unknown":true}`, "application/scim+json", 400},
		{http.MethodPost, "/scim/v2/Users", `{"schemas":["` + scim.UserSchema + `"],"externalId":"e","userName":"ada","name":{"givenName":"Ada","unknown":true}}`, "application/scim+json", 400},
		{http.MethodPost, "/scim/v2/Users", strings.Repeat("x", (1<<20)+1), "application/scim+json", 413},
		{http.MethodPatch, "/scim/v2/Users/1", string(patch), "application/scim+json", 400},
		{http.MethodPatch, "/scim/v2/Users/1", `{"schemas":["` + scim.PatchSchema + `"]}`, "application/scim+json", 400},
		{http.MethodPatch, "/scim/v2/Users/1", `{"schemas":["` + scim.PatchSchema + `"],"Operations":[]}`, "application/scim+json", 400},
		{http.MethodGet, "/scim/v2/Users?filter=" + strings.Repeat("x", 4097), "", "", 400},
		{http.MethodGet, "/scim/v2/Users?filter=userName%20eq%20%22ada%22&filter=userName%20eq%20%22grace%22", "", "", 400},
		{http.MethodGet, "/scim/v2/Users?filter=%zz", "", "", 400},
		{http.MethodGet, "/scim/v2/Users?" + strings.Repeat("x", 8193), "", "", 400},
		{http.MethodGet, "/scim/v2/" + strings.Repeat("x", (16<<10)+1), "", "", 400},
		{http.MethodGet, "/scim/v2/Users?sortBy=userName", "", "", 400},
	} {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
		if test.contentType != "" {
			request.Header.Set("Content-Type", test.contentType)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s status=%d body=%q", test.path, response.Code, response.Body.String())
		}
	}
}

func TestSCIMV2RejectsMissingPatchOperationsBeforeStore(t *testing.T) {
	store := &scimHTTPStore{}
	handler := scimTestHandler(t, store)
	for _, body := range []string{
		`{"schemas":["` + scim.PatchSchema + `"]}`,
		`{"schemas":["` + scim.PatchSchema + `"],"Operations":[]}`,
	} {
		response := scimRequest(handler, http.MethodPatch, "/scim/v2/Users/1", body, "valid")
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"scimType":"invalidValue"`) {
			t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
		}
	}
	if store.patchUsers != 0 {
		t.Fatalf("PatchUser calls=%d", store.patchUsers)
	}
}

func TestSCIMV2PreservesRFCErrorTypesAndMethodPrecedence(t *testing.T) {
	handler := scimTestHandler(t, &scimHTTPStore{})
	for _, test := range []struct {
		method, path, scimType, allow string
		status                        int
	}{
		{http.MethodGet, `/scim/v2/Users?filter=unsupported%20eq%20%22x%22`, "invalidFilter", "", 400},
		{http.MethodGet, `/scim/v2/Users?attributes=unsupported`, "invalidPath", "", 400},
		{http.MethodPost, `/scim/v2/ServiceProviderConfig?x=1`, "", "GET", 405},
		{http.MethodPost, `/scim/v2/Users/not-an-id`, "", "GET, PUT, PATCH, DELETE", 405},
	} {
		response := scimRequest(handler, test.method, test.path, `{}`, "valid")
		var got scim.Error
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if response.Code != test.status || got.SCIMType != test.scimType || response.Header().Get("Allow") != test.allow {
			t.Fatalf("%s status=%d type=%q allow=%q", test.path, response.Code, got.SCIMType, response.Header().Get("Allow"))
		}
	}
}

func scimTestHandler(t *testing.T, store scim.Store) http.Handler {
	t.Helper()
	authenticator, err := authn.NewProvisioningAuthenticator([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	return GuardSCIMV2(mux, authenticator, &scim.Service{Store: store, BaseURL: "https://grepnest.example", MaxResults: 100})
}

func scimRequest(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/scim+json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type scimHTTPStore struct{ patchUsers int }

func (*scimHTTPStore) ListUsers(context.Context, scim.Filter, scim.Page) ([]scim.User, int, error) {
	return []scim.User{{ID: "1", ExternalID: "e", UserName: "ada"}}, 1, nil
}
func (*scimHTTPStore) User(context.Context, int64) (scim.User, error) {
	return scim.User{ID: "1", ExternalID: "e", UserName: "ada"}, nil
}
func (*scimHTTPStore) CreateUser(_ context.Context, user scim.User) (scim.User, error) {
	user.ID = "1"
	return user, nil
}
func (*scimHTTPStore) ReplaceUser(_ context.Context, _ int64, user scim.User) (scim.User, error) {
	user.ID = "1"
	return user, nil
}
func (s *scimHTTPStore) PatchUser(context.Context, int64, scim.UserMutation) (scim.User, error) {
	s.patchUsers++
	return scim.User{ID: "1", ExternalID: "e", UserName: "ada"}, nil
}
func (*scimHTTPStore) DeleteUser(context.Context, int64) error { return nil }
func (*scimHTTPStore) ListGroups(context.Context, scim.Filter, scim.Page) ([]scim.Group, int, error) {
	return []scim.Group{}, 0, nil
}
func (*scimHTTPStore) Group(context.Context, int64) (scim.Group, error) {
	return scim.Group{}, errors.New("unused")
}
func (*scimHTTPStore) CreateGroup(context.Context, scim.Group) (scim.Group, error) {
	return scim.Group{}, errors.New("unused")
}
func (*scimHTTPStore) ReplaceGroup(context.Context, int64, scim.Group) (scim.Group, error) {
	return scim.Group{}, errors.New("unused")
}
func (*scimHTTPStore) PatchGroup(context.Context, int64, scim.GroupMutation) (scim.Group, error) {
	return scim.Group{}, errors.New("unused")
}
func (*scimHTTPStore) DeleteGroup(context.Context, int64) error { return errors.New("unused") }
func (store *scimHTTPStore) CreateUserAudited(ctx context.Context, user scim.User, _ []audit.Event) (scim.User, error) {
	return store.CreateUser(ctx, user)
}
func (store *scimHTTPStore) ReplaceUserAudited(ctx context.Context, id int64, user scim.User, _ []audit.Event) (scim.User, error) {
	return store.ReplaceUser(ctx, id, user)
}
func (store *scimHTTPStore) PatchUserAudited(ctx context.Context, id int64, mutation scim.UserMutation, _ []audit.Event) (scim.User, error) {
	return store.PatchUser(ctx, id, mutation)
}
func (store *scimHTTPStore) DeleteUserAudited(ctx context.Context, id int64, _ []audit.Event) error {
	return store.DeleteUser(ctx, id)
}
func (store *scimHTTPStore) CreateGroupAudited(ctx context.Context, group scim.Group, _ []audit.Event) (scim.Group, error) {
	return store.CreateGroup(ctx, group)
}
func (store *scimHTTPStore) ReplaceGroupAudited(ctx context.Context, id int64, group scim.Group, _ []audit.Event) (scim.Group, error) {
	return store.ReplaceGroup(ctx, id, group)
}
func (store *scimHTTPStore) PatchGroupAudited(ctx context.Context, id int64, mutation scim.GroupMutation, _ []audit.Event) (scim.Group, error) {
	return store.PatchGroup(ctx, id, mutation)
}
func (store *scimHTTPStore) DeleteGroupAudited(ctx context.Context, id int64, _ []audit.Event) error {
	return store.DeleteGroup(ctx, id)
}
