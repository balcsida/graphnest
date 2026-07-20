package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

type repositoryList struct {
	Repositories []api.RepositorySummary `json:"repositories"`
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

func RegisterRepositories(mux *http.ServeMux, authenticator authn.Authenticator, service *repository.Service, maxRequestBytes int64) {
	mux.Handle("/v1/repositories", exactMethod(http.MethodGet, AuthenticateBearer(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		repositories, err := service.List(request.Context(), PrincipalFromContext(request.Context()))
		if err != nil {
			writeRepositoryError(writer, err)
			return
		}
		if repositories == nil {
			repositories = []api.RepositorySummary{}
		}
		writeJSON(writer, repositoryList{Repositories: repositories})
	}))))
	mux.Handle("/v1/repositories/", exactMethod(http.MethodGet, AuthenticateBearer(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
		writeJSON(writer, response)
	}))))
	mux.Handle("/v1/files/read", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
		writeJSON(writer, response)
	}))))
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

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
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
