package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/grepnest/grepnest/internal/authn"
)

const maxAccessRepositories = 100

type directAccessRequest struct {
	Administrator *bool    `json:"direct_administrator"`
	RepositoryIDs *[]int64 `json:"direct_repository_ids"`
}

type accessRequest struct {
	Administrator *bool    `json:"administrator"`
	RepositoryIDs *[]int64 `json:"repository_ids"`
}

func registerAdminIdentity(mux *http.ServeMux, authenticator authn.RequestAuthenticator, service *admin.Service, maxRequestBytes, maxResponseBytes int64) {
	get := func(load func(*http.Request) (any, error)) http.Handler {
		return exactMethod(http.MethodGet, AuthenticateRequest(authenticator, administratorOnly(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			value, err := load(request)
			if err != nil {
				writeAdminError(writer, err)
				return
			}
			writeBoundedJSON(writer, value, maxResponseBytes)
		}))))
	}
	mux.Handle("/v1/admin/users", get(func(request *http.Request) (any, error) {
		users, truncated, err := service.Users(request.Context(), PrincipalFromContext(request.Context()))
		return struct {
			Users     []admin.User `json:"users"`
			Truncated bool         `json:"truncated"`
		}{users, truncated}, err
	}))
	mux.Handle("/v1/admin/groups", get(func(request *http.Request) (any, error) {
		groups, truncated, err := service.Groups(request.Context(), PrincipalFromContext(request.Context()))
		return struct {
			Groups    []admin.Group `json:"groups"`
			Truncated bool          `json:"truncated"`
		}{groups, truncated}, err
	}))
	mux.Handle("/v1/admin/users/", adminIdentityResource(authenticator, func(writer http.ResponseWriter, request *http.Request) {
		principal := PrincipalFromContext(request.Context())
		if id, ok := adminPathID(request.URL.Path, "/v1/admin/users/", ""); ok {
			if !adminIdentityMethod(writer, request, http.MethodGet) {
				return
			}
			user, err := service.User(request.Context(), principal, id)
			if err != nil {
				writeAdminError(writer, err)
				return
			}
			writeBoundedJSON(writer, user, maxResponseBytes)
			return
		}
		if id, ok := adminPathID(request.URL.Path, "/v1/admin/users/", "/access"); ok {
			if !adminIdentityMethod(writer, request, http.MethodPut) {
				return
			}
			input, ok := decodeDirectAccessRequest(writer, request, maxRequestBytes)
			if !ok {
				return
			}
			if err := service.ReplaceUserAccess(request.Context(), principal, id, *input.Administrator, *input.RepositoryIDs); err != nil {
				writeAdminError(writer, err)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		for suffix, suspended := range map[string]bool{"/suspend": true, "/restore": false} {
			if id, ok := adminPathID(request.URL.Path, "/v1/admin/users/", suffix); ok {
				if !adminIdentityAction(writer, request, maxRequestBytes) {
					return
				}
				if err := service.SuspendUser(request.Context(), principal, id, suspended); err != nil {
					writeAdminError(writer, err)
					return
				}
				writer.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if id, ok := adminPathID(request.URL.Path, "/v1/admin/users/", "/revoke-credentials"); ok {
			if !adminIdentityAction(writer, request, maxRequestBytes) {
				return
			}
			if err := service.RevokeUserCredentials(request.Context(), principal, id); err != nil {
				writeAdminError(writer, err)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
	}))
	mux.Handle("/v1/admin/groups/", adminIdentityResource(authenticator, func(writer http.ResponseWriter, request *http.Request) {
		principal := PrincipalFromContext(request.Context())
		if id, ok := adminPathID(request.URL.Path, "/v1/admin/groups/", ""); ok {
			if !adminIdentityMethod(writer, request, http.MethodGet) {
				return
			}
			group, err := service.Group(request.Context(), principal, id)
			if err != nil {
				writeAdminError(writer, err)
				return
			}
			writeBoundedJSON(writer, group, maxResponseBytes)
			return
		}
		if id, ok := adminPathID(request.URL.Path, "/v1/admin/groups/", "/access"); ok {
			if !adminIdentityMethod(writer, request, http.MethodPut) {
				return
			}
			input, ok := decodeAccessRequest(writer, request, maxRequestBytes)
			if !ok {
				return
			}
			if err := service.ReplaceGroupAccess(request.Context(), principal, id, *input.Administrator, *input.RepositoryIDs); err != nil {
				writeAdminError(writer, err)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
	}))
}

func adminIdentityResource(authenticator authn.RequestAuthenticator, handler http.HandlerFunc) http.Handler {
	return AuthenticateRequest(authenticator, administratorOnly(handler))
}

func adminIdentityMethod(writer http.ResponseWriter, request *http.Request, method string) bool {
	if request.Method == method {
		return true
	}
	writer.Header().Set("Allow", method)
	writeError(writer, http.StatusMethodNotAllowed, "invalid_request", "request is invalid", false)
	return false
}

func adminIdentityAction(writer http.ResponseWriter, request *http.Request, maxRequestBytes int64) bool {
	if !adminIdentityMethod(writer, request, http.MethodPost) {
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxRequestBytes))
	if err != nil || len(body) != 0 {
		writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
		return false
	}
	return true
}

func decodeAccessRequest(writer http.ResponseWriter, request *http.Request, maxRequestBytes int64) (accessRequest, bool) {
	var input accessRequest
	if !decodeAdminIdentityJSON(writer, request, maxRequestBytes, &input) {
		return accessRequest{}, false
	}
	if input.Administrator == nil || input.RepositoryIDs == nil || !validAccessRepositoryIDs(*input.RepositoryIDs) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
		return accessRequest{}, false
	}
	return input, true
}

func decodeDirectAccessRequest(writer http.ResponseWriter, request *http.Request, maxRequestBytes int64) (directAccessRequest, bool) {
	var input directAccessRequest
	if !decodeAdminIdentityJSON(writer, request, maxRequestBytes, &input) {
		return directAccessRequest{}, false
	}
	if input.Administrator == nil || input.RepositoryIDs == nil || !validAccessRepositoryIDs(*input.RepositoryIDs) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
		return directAccessRequest{}, false
	}
	return input, true
}

func decodeAdminIdentityJSON(writer http.ResponseWriter, request *http.Request, maxRequestBytes int64, input any) bool {
	if request.Header.Get("Content-Type") != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "invalid_request", "request is invalid", false)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
		return false
	}
	return true
}

func validAccessRepositoryIDs(ids []int64) bool {
	if len(ids) > maxAccessRepositories {
		return false
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id < 1 {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}
