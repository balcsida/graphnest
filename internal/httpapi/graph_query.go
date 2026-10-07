package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/pkg/api"
)

func RegisterGraphQueries(mux *http.ServeMux, authenticator authn.Authenticator, service *graphservice.Service, maxRequestBytes, maxResponseBytes int64) {
	mux.Handle("/v1/graph/context", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.GraphContextRequest) {
		response, err := service.Context(request.Context(), PrincipalFromContext(request.Context()), input)
		if err != nil {
			writeGraphQueryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
	mux.Handle("/v1/graph/impact", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.GraphImpactRequest) {
		response, err := service.Impact(request.Context(), PrincipalFromContext(request.Context()), input)
		if err != nil {
			writeGraphQueryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
	mux.Handle("/v1/graph/trace", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.GraphTraceRequest) {
		response, err := service.Trace(request.Context(), PrincipalFromContext(request.Context()), input)
		if err != nil {
			writeGraphQueryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
	mux.Handle("/v1/graph/discover", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.GraphDiscoverRequest) {
		response, err := service.Discover(request.Context(), PrincipalFromContext(request.Context()), input)
		if err != nil {
			writeGraphQueryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
	mux.Handle("/v1/graph/explore", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.GraphExploreRequest) {
		response, err := service.ExplorePublic(request.Context(), PrincipalFromContext(request.Context()), input)
		if err != nil {
			writeGraphQueryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
	mux.Handle("/v1/graph/files", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.GraphFilesRequest) {
		response, err := service.ListFilesPublic(request.Context(), PrincipalFromContext(request.Context()), input)
		if err != nil {
			writeGraphQueryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
	for path, query := range map[string]func(context.Context, authn.Principal, api.GraphSymbolCallsRequest) (graphprotocol.SymbolResponse, error){
		"/v1/graph/callers": service.SymbolCallers,
		"/v1/graph/callees": service.SymbolCallees,
	} {
		mux.Handle(path, exactMethod(http.MethodPost, AuthenticateBearer(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.GraphSymbolCallsRequest) {
			response, err := query(request.Context(), PrincipalFromContext(request.Context()), input)
			if err != nil {
				writeGraphQueryError(writer, err)
				return
			}
			writeBoundedJSON(writer, response, maxResponseBytes)
		}))))
	}
	mux.Handle("/v1/graph/impact-radius", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.GraphSymbolImpactRequest) {
		response, err := service.SymbolImpact(request.Context(), PrincipalFromContext(request.Context()), input)
		if err != nil {
			writeGraphQueryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
	mux.Handle("/v1/graph/capabilities", exactMethod(http.MethodPost, AuthenticateBearer(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.GraphCapabilitiesRequest) {
		response, err := service.Capabilities(request.Context(), PrincipalFromContext(request.Context()), input)
		if err != nil {
			writeGraphQueryError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))))
}

// GraphStateMessage explains a graph readiness failure and what resolves it.
// Messages are composed here from the sentinel and, for a missing generation,
// the indexed commit GraphNest itself resolved; error text is never echoed.
func GraphStateMessage(err error) string {
	var missing *graphquery.MissingGenerationError
	switch {
	case errors.Is(err, graphservice.ErrNotIndexed):
		return "repository is not indexed yet: graph queries need an indexed default branch; wait for indexing to finish"
	case errors.As(err, &missing):
		return "no graph generation is active for indexed commit " + missing.Commit + "; upload a SCIP index for that commit (POST /v1/scip/uploads) or publish a graph artifact (POST /v1/graph/uploads), then retry"
	case errors.Is(err, graphquery.ErrGraphMissing):
		return "no graph generation is active for the indexed commit; upload a SCIP index for it (POST /v1/scip/uploads) or publish a graph artifact (POST /v1/graph/uploads), then retry"
	case errors.Is(err, graphquery.ErrGenerationChanged):
		return "graph generation changed during the request: the repository was re-indexed or its graph was replaced; retry"
	case errors.Is(err, graphquery.ErrDiscoveryUnavailable):
		return "graph discovery projection is unavailable for the active generation: an operator must rebuild it (see the graph operation runbook)"
	default:
		return "graph is not ready: graph state is inconsistent for this request"
	}
}

func writeGraphQueryError(writer http.ResponseWriter, err error) {
	status, code, message, retryable := classifyGraphQueryError(err)
	writeError(writer, status, code, message, retryable)
}

func classifyGraphQueryError(err error) (int, string, string, bool) {
	switch {
	case errors.Is(err, graphservice.ErrInvalidRequest), errors.Is(err, graphservice.ErrInvalidRepositorySelector), errors.Is(err, graphquery.ErrInvalidRequest):
		return http.StatusBadRequest, "invalid_request", "request is invalid", false
	case errors.Is(err, authn.ErrUnauthenticated):
		return http.StatusUnauthorized, "unauthenticated", "authentication required", false
	case errors.Is(err, graphservice.ErrRepositoryNotFound):
		return http.StatusNotFound, "not_found", "repository not found", false
	case errors.Is(err, graphservice.ErrRepositoryRequired):
		return http.StatusConflict, "ambiguous", "repository selection is ambiguous", false
	case errors.Is(err, graphservice.ErrBranchNotIndexed):
		return http.StatusConflict, "branch_not_indexed", "branch is not indexed", false
	case errors.Is(err, graphservice.ErrNotIndexed):
		return http.StatusConflict, "not_indexed", GraphStateMessage(err), false
	case errors.Is(err, graphquery.ErrGraphMissing):
		return http.StatusConflict, "graph_missing", GraphStateMessage(err), false
	case errors.Is(err, graphquery.ErrGenerationChanged):
		return http.StatusConflict, "generation_changed", GraphStateMessage(err), true
	case errors.Is(err, graphquery.ErrDiscoveryUnavailable):
		return http.StatusConflict, "discovery_unavailable", GraphStateMessage(err), false
	case errors.Is(err, graphservice.ErrGraphNotReady):
		return http.StatusConflict, "graph_not_ready", GraphStateMessage(err), false
	case errors.Is(err, graphquery.ErrQuerySize):
		return http.StatusRequestEntityTooLarge, "response_too_large", "graph query response is too large", false
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "graph query timed out", true
	default:
		return http.StatusServiceUnavailable, "unavailable", "graph service is unavailable", true
	}
}
