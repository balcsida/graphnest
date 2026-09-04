package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

type repositoryList struct {
	Repositories []api.RepositorySummary `json:"repositories"`
	Truncated    bool                    `json:"truncated"`
	NextCursor   string                  `json:"next_cursor,omitempty"`
}

// repositoryCursor is the opaque page token of GET /v1/repositories: the full
// name of the last repository returned. Pages are ordered by full name, so a
// stale cursor still resumes at the right place after additions or removals.
type repositoryCursor struct {
	Version int    `json:"v"`
	Name    string `json:"name"`
}

// DecodeRepositoryCursor returns the full name a repository page cursor points
// past; an empty cursor is valid and means the first page.
func DecodeRepositoryCursor(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", false
	}
	var cursor repositoryCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&cursor); err != nil {
		return "", false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || cursor.Version != 1 || cursor.Name == "" {
		return "", false
	}
	return cursor.Name, true
}

// EncodeRepositoryCursor builds the opaque cursor that resumes after name.
func EncodeRepositoryCursor(name string) string {
	data, _ := json.Marshal(repositoryCursor{Version: 1, Name: name})
	return base64.RawURLEncoding.EncodeToString(data)
}

type readFileRequest struct {
	RepositoryID *int64              `json:"repository_id"`
	Path         *string             `json:"path"`
	StartLine    optionalPositiveInt `json:"start_line"`
	EndLine      optionalPositiveInt `json:"end_line"`
}

type optionalPositiveInt struct {
	value int
	set   bool
}

func (number *optionalPositiveInt) UnmarshalJSON(data []byte) error {
	number.set = true
	if string(data) == "null" || json.Unmarshal(data, &number.value) != nil || number.value < 1 {
		return errors.New("expected positive integer")
	}
	return nil
}

func RegisterRepositories(mux *http.ServeMux, authenticator authn.RequestAuthenticator, service *repository.Service, maxRequestBytes int64, maxResults int, maxResponseBytes int64) {
	RegisterRepositoryInventory(mux, authenticator, service, maxResults, maxResponseBytes)
	RegisterFileReads(mux, authenticator, service, maxRequestBytes, maxResponseBytes)
}

func RegisterRepositoryInventory(mux *http.ServeMux, authenticator authn.RequestAuthenticator, service *repository.Service, maxResults int, maxResponseBytes int64) {
	mux.Handle("/v1/repositories", exactMethod(http.MethodGet, AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		after, ok := DecodeRepositoryCursor(query.Get("cursor"))
		if !ok || (query.Has("cursor") && query.Get("cursor") == "") {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		repositories, err := service.List(request.Context(), PrincipalFromContext(request.Context()))
		if err != nil {
			writeRepositoryError(writer, err)
			return
		}
		response := limitRepositoryList(PageRepositoriesAfter(repositories, after), maxResults, maxResponseBytes)
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
	mux.Handle("/v1/repositories/", exactMethod(http.MethodGet, AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		id, ok := repositoryID(request.URL.Path)
		if !ok {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		response, err := service.Status(request.Context(), PrincipalFromContext(request.Context()), id)
		if err != nil {
			writeRepositoryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
}

func RegisterFileReads(mux *http.ServeMux, authenticator authn.RequestAuthenticator, service *repository.Service, maxRequestBytes int64, maxResponseBytes int64) {
	mux.Handle("/v1/files/read", exactMethod(http.MethodPost, AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Content-Type") != "application/json" {
			writeError(writer, http.StatusUnsupportedMediaType, "invalid_request", "request is invalid", false)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var input readFileRequest
		if err := decoder.Decode(&input); err != nil {
			writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
			return
		}
		if input.RepositoryID == nil || *input.RepositoryID < 1 || input.Path == nil || *input.Path == "" {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		fileRequest := api.ReadFileRequest{RepositoryID: *input.RepositoryID, Path: *input.Path}
		if input.StartLine.set {
			fileRequest.StartLine = input.StartLine.value
		}
		if input.EndLine.set {
			fileRequest.EndLine = input.EndLine.value
		}
		response, err := service.ReadFile(request.Context(), PrincipalFromContext(request.Context()), fileRequest)
		if err != nil {
			writeRepositoryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
}

// PageRepositoriesAfter orders the authorized repositories by full name and drops those at
// or before the cursor. The authorized set is already bounded by the
// principal's grants, so paging in memory costs nothing extra.
func PageRepositoriesAfter(repositories []api.RepositorySummary, after string) []api.RepositorySummary {
	sorted := slices.Clone(repositories)
	slices.SortStableFunc(sorted, func(a, b api.RepositorySummary) int { return strings.Compare(a.Name, b.Name) })
	if after == "" {
		return sorted
	}
	start, _ := slices.BinarySearchFunc(sorted, after, func(candidate api.RepositorySummary, target string) int {
		return strings.Compare(candidate.Name, target)
	})
	for start < len(sorted) && sorted[start].Name <= after {
		start++
	}
	return sorted[start:]
}

func limitRepositoryList(repositories []api.RepositorySummary, maxResults int, maxResponseBytes int64) repositoryList {
	result := repositoryList{Repositories: []api.RepositorySummary{}}
	for index, repository := range repositories {
		if len(result.Repositories) == maxResults {
			result.Truncated = true
			break
		}
		// A candidate is sized exactly as it would be sent: the cursor is part
		// of the envelope only while more repositories remain.
		candidate := repositoryList{Repositories: append(result.Repositories, repository), Truncated: index+1 < len(repositories)}
		if candidate.Truncated {
			candidate.NextCursor = EncodeRepositoryCursor(repository.Name)
		}
		data, _ := json.Marshal(candidate)
		if int64(len(data)+1) > maxResponseBytes {
			result.Truncated = true
			break
		}
		result = candidate
	}
	if result.Truncated && len(result.Repositories) > 0 {
		result.NextCursor = EncodeRepositoryCursor(result.Repositories[len(result.Repositories)-1].Name)
	} else {
		result.NextCursor = ""
	}
	return result
}

func exactMethod(method string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != method {
			writer.Header().Set("Allow", method)
			writeError(writer, http.StatusMethodNotAllowed, "invalid_request", "request is invalid", false)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func repositoryID(requestPath string) (int64, bool) {
	value := strings.TrimPrefix(requestPath, "/v1/repositories/")
	value = strings.TrimSuffix(value, "/status")
	if value == "" || strings.Contains(value, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func writeBoundedJSON(writer http.ResponseWriter, value any, maxBytes int64) {
	writeBoundedJSONStatus(writer, http.StatusOK, value, maxBytes)
}

func writeBoundedJSONStatus(writer http.ResponseWriter, status int, value any, maxBytes int64) {
	data, err := json.Marshal(value)
	if err != nil || int64(len(data)+1) > maxBytes {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(data, '\n'))
}

func writeRepositoryError(writer http.ResponseWriter, err error) {
	status, code, message, retryable := classifyRepositoryError(err)
	writeError(writer, status, code, message, retryable)
}

func RepositoryErrorMessage(err error) string {
	_, _, message, _ := classifyRepositoryError(err)
	return message
}

func classifyRepositoryError(err error) (int, string, string, bool) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return http.StatusNotFound, "not_found", "repository not found", false
	case errors.Is(err, repository.ErrNotIndexed):
		return http.StatusConflict, "not_indexed", "repository is not indexed", false
	case errors.Is(err, repository.ErrInvalidPath), errors.Is(err, repository.ErrInvalidRange), errors.Is(err, repository.ErrLineOutOfRange):
		return http.StatusBadRequest, "invalid_request", "file request is invalid", false
	case errors.Is(err, repository.ErrFileTooLarge):
		return http.StatusRequestEntityTooLarge, "file_too_large", "file is too large", false
	case errors.Is(err, repository.ErrInvalidFile), errors.Is(err, repository.ErrBinaryFile):
		return http.StatusUnprocessableEntity, "invalid_file", "file is unavailable", false
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "repository request timed out", true
	default:
		return http.StatusServiceUnavailable, "unavailable", "repository service is unavailable", true
	}
}
