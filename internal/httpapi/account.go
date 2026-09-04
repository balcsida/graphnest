package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/account"
	"github.com/balcsida/graphnest/internal/authn"
)

const maxTokenRepositories = 100

type createTokenRequest struct {
	ExpiresAt     *string `json:"expires_at"`
	RepositoryIDs []int64 `json:"repository_ids"`
}

func RegisterAccount(mux *http.ServeMux, authenticator authn.RequestAuthenticator, service *account.Service, maxRequestBytes, maxResponseBytes int64) {
	list := AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		items, err := service.Tokens(request.Context(), PrincipalFromContext(request.Context()))
		if err != nil {
			writeAccountError(writer, err)
			return
		}
		writeBoundedJSON(writer, struct {
			Tokens []account.Token `json:"tokens"`
		}{items}, maxResponseBytes)
	}))
	// mint parses a token-creation body and answers with the created token;
	// issue decides which service path (interactive self-service or
	// administrator delegation) performs the creation.
	mint := func(issue func(*http.Request, *time.Time, []int64) (account.Token, string, error)) http.Handler {
		return AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Content-Type") != "application/json" {
				writeError(writer, http.StatusUnsupportedMediaType, "invalid_request", "request is invalid", false)
				return
			}
			request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			var input createTokenRequest
			if err := decoder.Decode(&input); err != nil {
				writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
				return
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
				return
			}
			expires, ok := tokenExpiry(input.ExpiresAt)
			if !ok || !validTokenRepositoryIDs(input.RepositoryIDs) {
				writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
				return
			}
			token, plaintext, err := issue(request, expires, input.RepositoryIDs)
			if err != nil {
				writeAccountError(writer, err)
				return
			}
			writeBoundedJSONStatus(writer, http.StatusCreated, struct {
				ID            int64      `json:"id"`
				Prefix        string     `json:"prefix"`
				RepositoryIDs []int64    `json:"repository_ids,omitempty"`
				CreatedAt     time.Time  `json:"created_at"`
				LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
				ExpiresAt     *time.Time `json:"expires_at,omitempty"`
				Token         string     `json:"token"`
			}{token.ID, token.Prefix, token.RepositoryIDs, token.CreatedAt, token.LastUsedAt, token.ExpiresAt, plaintext}, maxResponseBytes)
		}))
	}
	create := mint(func(request *http.Request, expires *time.Time, repositoryIDs []int64) (account.Token, string, error) {
		return service.CreateToken(request.Context(), PrincipalFromContext(request.Context()), expires, repositoryIDs)
	})
	// Administrators authenticated with an API token delegate a narrower,
	// short-lived token for one job (for example a CI step uploading a SCIP
	// index for a single repository).
	mux.Handle("/v1/admin/api-tokens", privateAuth(exactMethod(http.MethodPost, mint(func(request *http.Request, expires *time.Time, repositoryIDs []int64) (account.Token, string, error) {
		return service.Delegate(request.Context(), PrincipalFromContext(request.Context()), expires, repositoryIDs)
	}))))
	mux.Handle("/v1/account/api-tokens", privateAuth(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			list.ServeHTTP(writer, request)
		case http.MethodPost:
			create.ServeHTTP(writer, request)
		default:
			writer.Header().Set("Allow", "GET, POST")
			writeError(writer, http.StatusMethodNotAllowed, "invalid_request", "request is invalid", false)
		}
	})))
	// Connected MCP clients: list and revoke the caller's OAuth grants.
	mux.Handle("/v1/account/oauth-grants", privateAuth(exactMethod(http.MethodGet, AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		grants, err := service.Grants(request.Context(), PrincipalFromContext(request.Context()))
		if err != nil {
			writeAccountError(writer, err)
			return
		}
		if grants == nil {
			grants = []account.Grant{}
		}
		writeBoundedJSON(writer, struct {
			Grants []account.Grant `json:"grants"`
		}{grants}, maxResponseBytes)
	})))))
	mux.Handle("/v1/account/oauth-grants/", privateAuth(exactMethod(http.MethodDelete, AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rest := strings.TrimPrefix(request.URL.Path, "/v1/account/oauth-grants/")
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || id < 1 || strings.Contains(rest, "/") {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		if err := service.RevokeGrant(request.Context(), PrincipalFromContext(request.Context()), id); err != nil {
			writeAccountError(writer, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})))))
	mux.Handle("/v1/account/api-tokens/", privateAuth(exactMethod(http.MethodDelete, AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		id, err := strconv.ParseInt(strings.TrimPrefix(request.URL.Path, "/v1/account/api-tokens/"), 10, 64)
		if err != nil || id < 1 || strings.Contains(strings.TrimPrefix(request.URL.Path, "/v1/account/api-tokens/"), "/") {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		if err := service.RevokeToken(request.Context(), PrincipalFromContext(request.Context()), id); err != nil {
			writeAccountError(writer, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})))))
}

func tokenExpiry(value *string) (*time.Time, bool) {
	if value == nil {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339) != *value {
		return nil, false
	}
	return &parsed, true
}

func validTokenRepositoryIDs(ids []int64) bool {
	if len(ids) > maxTokenRepositories {
		return false
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id < 1 {
			return false
		}
		if _, ok := seen[id]; ok {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func writeAccountError(writer http.ResponseWriter, err error) {
	if errors.Is(err, account.ErrInvalid) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
		return
	}
	if errors.Is(err, account.ErrForbidden) {
		writeError(writer, http.StatusForbidden, "forbidden", "forbidden", false)
		return
	}
	writeError(writer, http.StatusServiceUnavailable, "unavailable", "service unavailable", true)
}
