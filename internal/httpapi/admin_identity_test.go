package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/grepnest/grepnest/internal/audit"
	"github.com/grepnest/grepnest/internal/authn"
)

func TestAdminAuditEventsAreBounded(t *testing.T) {
	store := &adminHTTPStore{auditEvents: []audit.Event{{
		ActorType: "user", ActorID: "7", TargetType: "user", TargetID: "8",
		AuthenticationMethod: "oidc", Operation: audit.OperationUserSuspended, Outcome: "success",
	}}}
	response := httptest.NewRecorder()
	adminIdentityMux(store, 4096).ServeHTTP(response, adminIdentityRequest(http.MethodGet, "/v1/admin/audit-events", ""))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"truncated":true`) ||
		!strings.Contains(response.Body.String(), `"operation":"user_suspended"`) {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestAdminIdentityRoutesExposeBoundedEffectiveAccess(t *testing.T) {
	store := &adminHTTPStore{}
	mux := adminIdentityMux(store, 4096)
	for path, want := range map[string]string{
		"/v1/admin/users":    `"truncated":true`,
		"/v1/admin/users/7":  `"user_name":"ada"`,
		"/v1/admin/groups":   `"member_count":2`,
		"/v1/admin/groups/9": `"display_name":"Engineering"`,
	} {
		request := adminIdentityRequest(http.MethodGet, path, "")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) || response.Body.Len() > 4096 {
			t.Fatalf("%s status=%d bytes=%d body=%q", path, response.Code, response.Body.Len(), response.Body.String())
		}
		if path == "/v1/admin/users/7" &&
			(!strings.Contains(response.Body.String(), `"administrator":true`) ||
				!strings.Contains(response.Body.String(), `"repository_ids":[101]`) ||
				!strings.Contains(response.Body.String(), `"direct_administrator":false`) ||
				!strings.Contains(response.Body.String(), `"direct_repository_ids":[]`)) {
			t.Fatalf("%s did not separate effective and direct access: %q", path, response.Body.String())
		}
	}
}

func TestAdminIdentityRoutesReplaceOnlyAccessFields(t *testing.T) {
	store := &adminHTTPStore{}
	mux := adminIdentityMux(store, 4096)

	request := adminIdentityRequest(http.MethodPut, "/v1/admin/users/8/access", `{"direct_administrator":true,"direct_repository_ids":[101]}`)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || store.identityActorID != 7 || store.identityUserID != 8 ||
		!store.administrator || len(store.repositoryIDs) != 1 || store.repositoryIDs[0] != 101 {
		t.Fatalf("status=%d actor=%d user=%d admin=%v repositories=%v body=%q",
			response.Code, store.identityActorID, store.identityUserID, store.administrator, store.repositoryIDs, response.Body.String())
	}

	request = adminIdentityRequest(http.MethodPut, "/v1/admin/groups/9/access", `{"administrator":false,"repository_ids":[]}`)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || store.identityGroupID != 9 || store.administrator || len(store.repositoryIDs) != 0 {
		t.Fatalf("status=%d group=%d admin=%v repositories=%v body=%q",
			response.Code, store.identityGroupID, store.administrator, store.repositoryIDs, response.Body.String())
	}

	store.identityUserID = 0
	request = adminIdentityRequest(http.MethodPut, "/v1/admin/users/8/access", `{"direct_administrator":true,"direct_repository_ids":[],"display_name":"Changed"}`)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || store.identityUserID != 0 {
		t.Fatalf("SCIM field status=%d mutated user=%d body=%q", response.Code, store.identityUserID, response.Body.String())
	}
}

func TestAdminIdentityRoutesSuspendRestoreAndRevoke(t *testing.T) {
	store := &adminHTTPStore{}
	mux := adminIdentityMux(store, 4096)
	for _, test := range []struct {
		path          string
		suspended     bool
		revokedUserID int64
	}{
		{"/v1/admin/users/8/suspend", true, 0},
		{"/v1/admin/users/8/restore", false, 0},
		{"/v1/admin/users/8/revoke-credentials", false, 8},
	} {
		request := adminIdentityRequest(http.MethodPost, test.path, "")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent || store.suspended != test.suspended || store.revokedUserID != test.revokedUserID {
			t.Fatalf("%s status=%d suspended=%v revoked=%d body=%q", test.path, response.Code, store.suspended, store.revokedUserID, response.Body.String())
		}
		store.revokedUserID = 0
	}
}

func TestAdminIdentityRoutesRejectMalformedRequests(t *testing.T) {
	for name, request := range map[string]*http.Request{
		"wrong method": adminIdentityRequest(http.MethodPost, "/v1/admin/users/8/access", ""),
		"wrong path":   adminIdentityRequest(http.MethodGet, "/v1/admin/users/8/extra", ""),
		"missing content type": adminIdentityRequest(http.MethodPut, "/v1/admin/users/8/access",
			`{"direct_administrator":true,"direct_repository_ids":[]}`),
		"missing field": func() *http.Request {
			request := adminIdentityRequest(http.MethodPut, "/v1/admin/users/8/access", `{"direct_administrator":true}`)
			request.Header.Set("Content-Type", "application/json")
			return request
		}(),
		"duplicate repository": func() *http.Request {
			request := adminIdentityRequest(http.MethodPut, "/v1/admin/users/8/access", `{"direct_administrator":true,"direct_repository_ids":[101,101]}`)
			request.Header.Set("Content-Type", "application/json")
			return request
		}(),
		"effective fields": func() *http.Request {
			request := adminIdentityRequest(http.MethodPut, "/v1/admin/users/8/access", `{"administrator":true,"repository_ids":[101]}`)
			request.Header.Set("Content-Type", "application/json")
			return request
		}(),
		"action body": adminIdentityRequest(http.MethodPost, "/v1/admin/users/8/suspend", `{}`),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			adminIdentityMux(&adminHTTPStore{}, 4096).ServeHTTP(response, request)
			if response.Code < 400 || response.Code >= 500 {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestAdminIdentityRoutesMapSafeConflicts(t *testing.T) {
	for _, failure := range []error{admin.ErrSelfAdministration, admin.ErrFinalAdministrator} {
		store := &adminHTTPStore{identityErr: failure}
		request := adminIdentityRequest(http.MethodPost, "/v1/admin/users/8/suspend", "")
		response := httptest.NewRecorder()
		adminIdentityMux(store, 4096).ServeHTTP(response, request)
		if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), failure.Error()) {
			t.Fatalf("failure=%v status=%d body=%q", failure, response.Code, response.Body.String())
		}
	}
}

func adminIdentityMux(store *adminHTTPStore, maxResponseBytes int64) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterAdmin(mux, requestAuthenticator(authn.NewStatic(map[string]authn.Principal{
		"admin": {Subject: "7", Method: "oidc", Administrator: true},
	})), &admin.Service{Store: store, GitHub: adminHTTPGitHub{}}, 1, 1024, maxResponseBytes)
	return mux
}

func adminIdentityRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin")
	return request
}

func (store *adminHTTPStore) AdminUsers(context.Context, int) ([]admin.User, bool, error) {
	return []admin.User{{ID: 7, ExternalID: "directory-7", UserName: "ada", Source: "scim", SCIMActive: true, Administrator: true, RepositoryIDs: []int64{101}, DirectRepositoryIDs: []int64{}}}, true, store.identityErr
}
func (store *adminHTTPStore) AdminUser(context.Context, int64) (admin.User, error) {
	return admin.User{ID: 7, ExternalID: "directory-7", UserName: "ada", Source: "scim", SCIMActive: true, Administrator: true, RepositoryIDs: []int64{101}, DirectRepositoryIDs: []int64{}}, store.identityErr
}
func (store *adminHTTPStore) AdminGroups(context.Context, int) ([]admin.Group, bool, error) {
	return []admin.Group{{ID: 9, ExternalID: "engineering", DisplayName: "Engineering", Administrator: true, RepositoryIDs: []int64{101}, MemberCount: 2}}, false, store.identityErr
}
func (store *adminHTTPStore) AdminGroup(context.Context, int64) (admin.Group, error) {
	return admin.Group{ID: 9, ExternalID: "engineering", DisplayName: "Engineering", Administrator: true, RepositoryIDs: []int64{101}, MemberCount: 2}, store.identityErr
}
func (store *adminHTTPStore) SuspendAdminUser(_ context.Context, actorID, userID int64, suspended bool) error {
	store.identityActorID, store.identityUserID, store.suspended = actorID, userID, suspended
	return store.identityErr
}
func (store *adminHTTPStore) ReplaceAdminUserAccess(_ context.Context, actorID, userID int64, administrator bool, repositoryIDs []int64) error {
	store.identityActorID, store.identityUserID, store.administrator = actorID, userID, administrator
	store.repositoryIDs = append([]int64(nil), repositoryIDs...)
	return store.identityErr
}
func (store *adminHTTPStore) ReplaceAdminGroupAccess(_ context.Context, actorID, groupID int64, administrator bool, repositoryIDs []int64) error {
	store.identityActorID, store.identityGroupID, store.administrator = actorID, groupID, administrator
	store.repositoryIDs = append([]int64(nil), repositoryIDs...)
	return store.identityErr
}
func (store *adminHTTPStore) RevokeAdminUserCredentials(_ context.Context, userID int64) error {
	store.revokedUserID = userID
	return store.identityErr
}
