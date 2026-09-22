package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/jackc/pgx/v5"
)

// RegisterSupplyChain mounts the Dependencies & Licenses inventory routes.
// Every route authenticates the request, then the service authorizes the
// repository with the live principal before touching inventory rows.
func RegisterSupplyChain(mux *http.ServeMux, authenticator authn.RequestAuthenticator, service *supplychain.Service, maxResults int, maxResponseBytes int64) {
	authenticated := func(method string, handle func(http.ResponseWriter, *http.Request)) http.Handler {
		return exactMethod(method, AuthenticateRequest(authenticator, http.HandlerFunc(handle)))
	}
	limitFrom := func(query map[string][]string) (int, bool) {
		values := query["limit"]
		if len(values) == 0 {
			return 0, true
		}
		if len(values) != 1 {
			return 0, false
		}
		limit, err := strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > maxResults {
			return 0, false
		}
		return limit, true
	}
	mux.Handle("/v1/supply-chain/repositories/", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		githubID, action, ok := supplyChainRepositoryPath(request.URL.Path)
		if !ok {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		query := request.URL.Query()
		stream := query.Get("stream")
		if !supplychain.ValidStream(stream) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		switch action {
		case "":
			authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
				response, err := service.Status(request.Context(), PrincipalFromContext(request.Context()), githubID, stream)
				if err != nil {
					writeSupplyChainError(writer, err)
					return
				}
				writeBoundedJSON(writer, response, maxResponseBytes)
			}).ServeHTTP(writer, request)
		case "components":
			authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
				limit, ok := limitFrom(query)
				var snapshotID int64
				if value := query.Get("snapshot_id"); value != "" {
					var err error
					snapshotID, err = strconv.ParseInt(value, 10, 64)
					ok = ok && err == nil && snapshotID > 0
				}
				if !ok || (query.Has("cursor") && query.Get("cursor") == "") {
					writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
					return
				}
				response, err := service.Components(request.Context(), PrincipalFromContext(request.Context()), githubID, stream, snapshotID, query.Get("cursor"), limit, query.Get("q"))
				if err != nil {
					writeSupplyChainError(writer, err)
					return
				}
				writeBoundedJSON(writer, response, maxResponseBytes)
			}).ServeHTTP(writer, request)
		case "snapshots":
			authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
				limit, ok := limitFrom(query)
				if !ok {
					writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
					return
				}
				snapshots, err := service.Snapshots(request.Context(), PrincipalFromContext(request.Context()), githubID, stream, limit)
				if err != nil {
					writeSupplyChainError(writer, err)
					return
				}
				writeBoundedJSON(writer, struct {
					Snapshots any `json:"snapshots"`
				}{snapshots}, maxResponseBytes)
			}).ServeHTTP(writer, request)
		case "collections":
			authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
				limit, ok := limitFrom(query)
				if !ok || (query.Has("cursor") && query.Get("cursor") == "") {
					writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
					return
				}
				response, err := service.Collections(request.Context(), PrincipalFromContext(request.Context()), githubID, stream, query.Get("cursor"), limit)
				if err != nil {
					writeSupplyChainError(writer, err)
					return
				}
				writeBoundedJSON(writer, response, maxResponseBytes)
			}).ServeHTTP(writer, request)
		case "refresh":
			authenticated(http.MethodPost, func(writer http.ResponseWriter, request *http.Request) {
				if request.ContentLength != 0 {
					writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
					return
				}
				response, err := service.Refresh(request.Context(), PrincipalFromContext(request.Context()), githubID, stream)
				if err != nil {
					writeSupplyChainError(writer, err)
					return
				}
				writeBoundedJSONStatus(writer, http.StatusAccepted, response, maxResponseBytes)
			}).ServeHTTP(writer, request)
		default:
			writeError(writer, http.StatusNotFound, "not_found", "route not found", false)
		}
	}))
	mux.Handle("/v1/supply-chain/snapshots/", authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
		snapshotID, ok := supplyChainSnapshotDocumentPath(request.URL.Path)
		if !ok {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		body, mediaType, digest, snapshot, err := service.Document(request.Context(), PrincipalFromContext(request.Context()), snapshotID)
		if err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		if mediaType == "" || strings.ContainsAny(mediaType, "\r\n") {
			mediaType = "application/json"
		}
		writer.Header().Set("Content-Type", mediaType)
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Cache-Control", "private, no-store")
		writer.Header().Set("Content-Disposition", `attachment; filename="`+supplyChainDocumentName(snapshot)+`"`)
		writer.Header().Set("X-Content-SHA256", digest)
		writer.Header().Set("X-GraphNest-Snapshot-ID", strconv.FormatInt(snapshot.ID, 10))
		writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(body)
	}))
	mux.Handle("/v1/supply-chain/jobs/", authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
		value := strings.TrimPrefix(request.URL.Path, "/v1/supply-chain/jobs/")
		jobID, err := strconv.ParseInt(value, 10, 64)
		if err != nil || jobID < 1 || strings.Contains(value, "/") {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		response, err := service.JobStatus(request.Context(), PrincipalFromContext(request.Context()), jobID)
		if err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))
}

// supplyChainRepositoryPath splits /v1/supply-chain/repositories/{id}[/{action}].
func supplyChainRepositoryPath(requestPath string) (int64, string, bool) {
	rest := strings.TrimPrefix(requestPath, "/v1/supply-chain/repositories/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" || strings.Contains(action, "/") {
		return 0, "", false
	}
	githubID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || githubID < 1 {
		return 0, "", false
	}
	return githubID, action, true
}

func supplyChainSnapshotDocumentPath(requestPath string) (int64, bool) {
	rest := strings.TrimPrefix(requestPath, "/v1/supply-chain/snapshots/")
	id, ok := strings.CutSuffix(rest, "/document")
	if !ok || id == "" || strings.Contains(id, "/") {
		return 0, false
	}
	snapshotID, err := strconv.ParseInt(id, 10, 64)
	return snapshotID, err == nil && snapshotID > 0
}

// supplyChainDocumentName builds a safe attachment filename from trusted
// fields only (numeric IDs, producer enum, format enum).
func supplyChainDocumentName(snapshot supplychain.Snapshot) string {
	extension := "json"
	return "graphnest-sbom-" + string(snapshot.Producer) + "-repo" + strconv.FormatInt(snapshot.RepositoryID, 10) + "-snapshot" + strconv.FormatInt(snapshot.ID, 10) + "." + extension
}

func writeSupplyChainError(writer http.ResponseWriter, err error) {
	status, code, message, retryable := classifySupplyChainError(err)
	writeError(writer, status, code, message, retryable)
}

// SupplyChainErrorMessage returns the safe message for MCP error results.
func SupplyChainErrorMessage(err error) string {
	_, _, message, _ := classifySupplyChainError(err)
	return message
}

func classifySupplyChainError(err error) (int, string, string, bool) {
	switch {
	case errors.Is(err, supplychain.ErrForbidden):
		return http.StatusForbidden, "forbidden", "administrator access required", false
	case errors.Is(err, supplychain.ErrInvalidRequest):
		return http.StatusBadRequest, "invalid_request", "request is invalid", false
	case errors.Is(err, supplychain.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		return http.StatusNotFound, "not_found", "not found", false
	case errors.Is(err, supplychain.ErrNoInventory):
		return http.StatusNotFound, "no_inventory", "no inventory has been collected for this repository stream", false
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "inventory request timed out", true
	default:
		return http.StatusServiceUnavailable, "unavailable", "inventory service is unavailable", true
	}
}
