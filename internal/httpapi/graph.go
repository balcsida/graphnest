package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphingest"
	"github.com/jackc/pgx/v5"
)

const graphContentType = "application/vnd.grepnest.graph.v1+protobuf"

func RegisterGraphIngestion(mux *http.ServeMux, authenticator authn.Authenticator, service *graphingest.Service, maxUploadBytes, maxResponseBytes int64) {
	mux.Handle("/v1/graph/uploads", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Content-Type") != graphContentType {
			writeError(writer, http.StatusUnsupportedMediaType, "invalid_request", "request is invalid", false)
			return
		}
		query := request.URL.Query()
		if len(query) != 2 || len(query["repository_id"]) != 1 || len(query["commit"]) != 1 {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		repositoryID, err := strconv.ParseInt(query.Get("repository_id"), 10, 64)
		commit := query.Get("commit")
		if err != nil || repositoryID < 1 || !scipCommitPattern.MatchString(commit) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		controller := http.NewResponseController(writer)
		setWriteDeadline := func() { _ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second)) }
		principal := PrincipalFromContext(request.Context())
		if err := service.ValidateExternalUpload(request.Context(), principal, repositoryID, commit); err != nil {
			setWriteDeadline()
			writeGraphError(writer, err)
			return
		}
		_ = controller.SetReadDeadline(time.Time{})
		data, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxUploadBytes))
		if err != nil {
			setWriteDeadline()
			writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
			return
		}
		if _, err := service.UploadExternal(request.Context(), principal, repositoryID, commit, data); err != nil {
			setWriteDeadline()
			writeGraphError(writer, err)
			return
		}
		setWriteDeadline()
		writer.WriteHeader(http.StatusNoContent)
	}))))

	mux.Handle("/v1/graph/repositories/", exactMethod(http.MethodGet, AuthenticateBearer(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		repositoryID, ok := graphStatusRepositoryID(request.URL.Path)
		if !ok || len(request.URL.Query()) != 0 {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		status, err := service.Status(request.Context(), PrincipalFromContext(request.Context()), repositoryID)
		if err != nil {
			writeGraphError(writer, err)
			return
		}
		writeBoundedJSON(writer, status, maxResponseBytes)
	}))))
}

func graphStatusRepositoryID(path string) (int64, bool) {
	const prefix, suffix = "/v1/graph/repositories/", "/status"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return 0, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if value == "" || strings.Contains(value, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func writeGraphError(writer http.ResponseWriter, err error) {
	status, code, message, retryable := classifyGraphError(err)
	writeError(writer, status, code, message, retryable)
}

func classifyGraphError(err error) (int, string, string, bool) {
	switch {
	case errors.Is(err, graphingest.ErrForbidden):
		return http.StatusForbidden, "forbidden", "administrator access required", false
	case errors.Is(err, graphingest.ErrInvalidArtifact):
		return http.StatusBadRequest, "invalid_request", "request is invalid", false
	case errors.Is(err, graphingest.ErrNotIndexed):
		return http.StatusConflict, "not_indexed", "repository is not indexed", false
	case errors.Is(err, pgx.ErrNoRows):
		return http.StatusNotFound, "not_found", "repository not found", false
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, graphingest.ErrUnavailable):
		return http.StatusServiceUnavailable, "unavailable", "graph service is unavailable", true
	default:
		return http.StatusServiceUnavailable, "unavailable", "graph service is unavailable", true
	}
}
