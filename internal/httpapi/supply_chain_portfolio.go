package httpapi

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/supplychain"
)

// RegisterSupplyChainPortfolio mounts the cross-repository inventory routes.
// Authorization scope is resolved from the live principal on every request
// before any aggregate is computed.
func RegisterSupplyChainPortfolio(mux *http.ServeMux, authenticator authn.RequestAuthenticator, portfolio *supplychain.Portfolio, maxResults int, maxResponseBytes int64) {
	authenticated := func(method string, handle func(http.ResponseWriter, *http.Request)) http.Handler {
		return exactMethod(method, AuthenticateRequest(authenticator, http.HandlerFunc(handle)))
	}
	mux.Handle("/v1/supply-chain/overview", authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		repositories, ok := repositoryIDsFrom(query["repository_id"])
		if !ok || !supplychain.ValidStream(query.Get("stream")) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		response, err := portfolio.Overview(request.Context(), PrincipalFromContext(request.Context()), query.Get("stream"), repositories)
		if err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))
	mux.Handle("/v1/supply-chain/facets", authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		repositories, ok := repositoryIDsFrom(query["repository_id"])
		if !ok || !supplychain.ValidStream(query.Get("stream")) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		response, err := portfolio.Facets(request.Context(), PrincipalFromContext(request.Context()), query.Get("stream"), repositories)
		if err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))
	mux.Handle("/v1/supply-chain/components", authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		repositories, ok := repositoryIDsFrom(query["repository_id"])
		limit := 0
		if value := query.Get("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			ok = ok && err == nil && parsed >= 1 && parsed <= maxResults
			limit = parsed
		}
		if !ok || !supplychain.ValidStream(query.Get("stream")) || (query.Has("cursor") && query.Get("cursor") == "") {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		response, err := portfolio.Components(request.Context(), PrincipalFromContext(request.Context()), supplychain.ComponentsRequest{
			Stream: query.Get("stream"), RepositoryIDs: repositories, Ecosystem: query.Get("ecosystem"), Search: query.Get("q"), License: query.Get("license"),
			AssessmentStatus: query.Get("assessment"), Cursor: query.Get("cursor"), Limit: limit,
		})
		if err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))
	mux.Handle("/v1/supply-chain/components/", authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
		key := strings.TrimPrefix(request.URL.Path, "/v1/supply-chain/components/")
		query := request.URL.Query()
		repositories, ok := repositoryIDsFrom(query["repository_id"])
		if !ok || key == "" || strings.Contains(key, "/") || len(key) > 4096 || !supplychain.ValidStream(query.Get("stream")) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		response, err := portfolio.Component(request.Context(), PrincipalFromContext(request.Context()), query.Get("stream"), key, repositories)
		if err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))
	// Repository-scoped export and comparison live under the repositories
	// namespace but are served here because they need the portfolio service.
	mux.Handle("/v1/supply-chain/exports/", authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
		rest := strings.TrimPrefix(request.URL.Path, "/v1/supply-chain/exports/")
		id, suffix, _ := strings.Cut(rest, "/")
		githubID, err := strconv.ParseInt(id, 10, 64)
		query := request.URL.Query()
		var snapshotID int64
		ok := err == nil && githubID > 0 && suffix == "components.csv" && supplychain.ValidStream(query.Get("stream"))
		if value := query.Get("snapshot_id"); value != "" && ok {
			snapshotID, err = strconv.ParseInt(value, 10, 64)
			ok = err == nil && snapshotID > 0
		}
		if !ok {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		var buffer bytes.Buffer
		snapshot, err := portfolio.ExportCSV(request.Context(), PrincipalFromContext(request.Context()), githubID, query.Get("stream"), snapshotID, &buffer)
		if err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		if int64(buffer.Len()) > maxResponseBytes*64 {
			writeError(writer, http.StatusRequestEntityTooLarge, "too_large", "export exceeds the response limit; use the component API with pagination", false)
			return
		}
		writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Cache-Control", "private, no-store")
		writer.Header().Set("Content-Disposition", `attachment; filename="graphnest-inventory-repo`+strconv.FormatInt(snapshot.RepositoryID, 10)+`-snapshot`+strconv.FormatInt(snapshot.ID, 10)+`.csv"`)
		writer.Header().Set("X-GraphNest-Snapshot-ID", strconv.FormatInt(snapshot.ID, 10))
		writer.Header().Set("Content-Length", strconv.Itoa(buffer.Len()))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(buffer.Bytes())
	}))
	mux.Handle("/v1/supply-chain/compare", authenticated(http.MethodGet, func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		githubID, err1 := strconv.ParseInt(query.Get("repository_id"), 10, 64)
		baseID, err2 := strconv.ParseInt(query.Get("base"), 10, 64)
		headID, err3 := strconv.ParseInt(query.Get("head"), 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		response, err := portfolio.Compare(request.Context(), PrincipalFromContext(request.Context()), githubID, baseID, headID)
		if err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))
}

// repositoryIDsFrom parses repeated repository_id query values (bounded).
func repositoryIDsFrom(values []string) ([]int64, bool) {
	if len(values) > 200 {
		return nil, false
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}
