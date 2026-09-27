package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphingest"
	"github.com/jackc/pgx/v5"
)

const (
	graphContentType   = "application/vnd.graphnest.graph.v1+protobuf"
	graphV2ContentType = "application/vnd.graphnest.graph.v2+protobuf"
)

// RegisterGraphIngestion mounts graph uploads and status.
//
//	POST /v1/graph/uploads?repository_id=101&commit=<sha>                                   (v1, administrators)
//	POST /v1/graph/uploads?repository_id=101&commit=<sha>&expected_generation=7[&replace_producer=true] (v2)
func RegisterGraphIngestion(mux *http.ServeMux, authenticator authn.Authenticator, service *graphingest.Service, maxUploadBytes, maxResponseBytes int64) {
	mux.Handle("/v1/graph/uploads", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		contentType := request.Header.Get("Content-Type")
		v2 := contentType == graphV2ContentType
		if !v2 && contentType != graphContentType {
			writeError(writer, http.StatusUnsupportedMediaType, "invalid_request", "request is invalid", false)
			return
		}
		repositoryID, commit, intent, ok := graphUploadQuery(request.URL.Query(), v2)
		if !ok {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		controller := http.NewResponseController(writer)
		setWriteDeadline := func() { _ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second)) }
		principal := PrincipalFromContext(request.Context())
		validate := service.ValidateExternalUpload
		if v2 {
			validate = service.ValidatePublication
		}
		if err := validate(request.Context(), principal, repositoryID, commit); err != nil {
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
		if v2 {
			result, err := service.Publish(request.Context(), principal, repositoryID, commit, data, intent)
			setWriteDeadline()
			if err != nil {
				writeGraphError(writer, err)
				return
			}
			writeBoundedJSON(writer, result, maxResponseBytes)
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

// graphUploadQuery accepts exactly repository_id and commit, plus
// expected_generation and an optional replace_producer=true for v2.
func graphUploadQuery(query url.Values, v2 bool) (int64, string, graphingest.Publication, bool) {
	var intent graphingest.Publication
	for key, values := range query {
		known := key == "repository_id" || key == "commit" || v2 && (key == "expected_generation" || key == "replace_producer")
		if !known || len(values) != 1 {
			return 0, "", intent, false
		}
	}
	repositoryID, err := strconv.ParseInt(query.Get("repository_id"), 10, 64)
	commit := query.Get("commit")
	if err != nil || repositoryID < 1 || !scipCommitPattern.MatchString(commit) {
		return 0, "", intent, false
	}
	if !v2 {
		return repositoryID, commit, intent, true
	}
	intent.ExpectedGeneration, err = strconv.ParseInt(query.Get("expected_generation"), 10, 64)
	if err != nil || intent.ExpectedGeneration < 0 {
		return 0, "", intent, false
	}
	if query.Has("replace_producer") {
		if query.Get("replace_producer") != "true" {
			return 0, "", intent, false
		}
		intent.ReplaceProducer = true
	}
	return repositoryID, commit, intent, true
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
		return http.StatusForbidden, "forbidden", "graph publication is not permitted", false
	case errors.Is(err, graphingest.ErrConflict):
		return http.StatusConflict, "generation_conflict", "graph generation or indexed commit changed", false
	case errors.Is(err, graphingest.ErrProducerConflict):
		return http.StatusConflict, "producer_conflict", "replacing another producer requires replace_producer=true", false
	case errors.Is(err, graphingest.ErrUnauthenticated):
		return http.StatusUnauthorized, "unauthenticated", "authentication required", false
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
