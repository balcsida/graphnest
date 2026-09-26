package httpapi

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/supplychain/license"
	"github.com/balcsida/graphnest/internal/supplychain/policy"
	"github.com/balcsida/graphnest/internal/supplychain/review"
	"github.com/balcsida/graphnest/pkg/api"
)

// RegisterSupplyChainReview mounts the review queue, conclusions, decisions,
// history, grants, policies, and audit routes.
func RegisterSupplyChainReview(mux *http.ServeMux, authenticator authn.RequestAuthenticator, service *review.Service, maxRequestBytes, maxResponseBytes int64) {
	authenticated := func(method string, handle http.Handler) http.Handler {
		return exactMethod(method, AuthenticateRequest(authenticator, handle))
	}
	mux.Handle("/v1/supply-chain/review/queue", authenticated(http.MethodGet, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		var after int64
		if value := query.Get("cursor"); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 1 {
				writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
				return
			}
			after = parsed
		}
		limit := 0
		if value := query.Get("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
				return
			}
			limit = parsed
		}
		items, truncated, err := service.Queue(request.Context(), PrincipalFromContext(request.Context()), query.Get("stream"), after, limit)
		if err != nil {
			writeReviewError(writer, err)
			return
		}
		response := api.SupplyChainReviewQueue{Items: make([]api.SupplyChainReviewItem, 0, len(items)), Truncated: truncated}
		for _, item := range items {
			response.Items = append(response.Items, queueItem(item))
		}
		if truncated && len(items) > 0 {
			response.NextCursor = strconv.FormatInt(items[len(items)-1].ComponentID, 10)
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	})))
	mux.Handle("/v1/supply-chain/review/conclusions", authenticated(http.MethodPost, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.SupplyChainConcludeRequest) {
		conclusion, err := service.Conclude(request.Context(), PrincipalFromContext(request.Context()), review.ConcludeRequest{
			RepositoryID: input.RepositoryID, Coordinates: coordinates(input.Coordinates), Expression: input.Expression, Reason: input.Reason, BasisFingerprint: input.BasisFingerprint,
		})
		if err != nil {
			writeReviewError(writer, err)
			return
		}
		writeBoundedJSONStatus(writer, http.StatusCreated, conclusionSummary(conclusion), maxResponseBytes)
	})))
	mux.Handle("/v1/supply-chain/review/decisions", authenticated(http.MethodPost, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.SupplyChainDecideRequest) {
		decision, err := service.Decide(request.Context(), PrincipalFromContext(request.Context()), review.DecideRequest{
			RepositoryID: input.RepositoryID, Coordinates: coordinates(input.Coordinates), Kind: review.DecisionKind(input.Kind), Reason: input.Reason, UsageContext: input.UsageContext,
			ExpiresAt: input.ExpiresAt, BasisFingerprint: input.BasisFingerprint,
		})
		if err != nil {
			writeReviewError(writer, err)
			return
		}
		writeBoundedJSONStatus(writer, http.StatusCreated, decisionSummary(decision), maxResponseBytes)
	})))
	mux.Handle("/v1/supply-chain/review/history", authenticated(http.MethodGet, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		githubID, err := strconv.ParseInt(query.Get("repository_id"), 10, 64)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		history, err := service.History(request.Context(), PrincipalFromContext(request.Context()), githubID, license.Coordinates{Ecosystem: query.Get("ecosystem"), Namespace: query.Get("namespace"), Name: query.Get("name"), Version: query.Get("version")})
		if err != nil {
			writeReviewError(writer, err)
			return
		}
		response := api.SupplyChainReviewHistory{Basis: history.Basis, Conclusions: []api.SupplyChainConclusion{}, Decisions: []api.SupplyChainDecision{}, PolicyResults: []api.SupplyChainPolicyResult{}, CurrentStale: history.CurrentStale, StaleReason: history.StaleReason}
		for _, conclusion := range history.Conclusions {
			response.Conclusions = append(response.Conclusions, conclusionSummary(conclusion))
		}
		for _, decision := range history.Decisions {
			response.Decisions = append(response.Decisions, decisionSummary(decision))
		}
		if history.Current != nil {
			current := decisionSummary(*history.Current)
			response.Current = &current
		}
		for _, result := range history.PolicyResults {
			response.PolicyResults = append(response.PolicyResults, api.SupplyChainPolicyResult{PolicyID: result.PolicyID, Verdict: string(result.Verdict), Explanation: result.Explanation, EvaluatedAt: result.EvaluatedAt, EvidenceFingerprint: hex.EncodeToString(result.EvidenceFingerprint)})
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	})))
	mux.Handle("/v1/supply-chain/review/grants", authenticated(http.MethodPut, jsonSCIPHandler(4<<10, func(writer http.ResponseWriter, request *http.Request, input struct {
		RepositoryID int64  `json:"repository_id"`
		Subject      string `json:"subject"`
		Allow        bool   `json:"allow"`
	}) {
		if err := service.SetReviewGrant(request.Context(), PrincipalFromContext(request.Context()), input.RepositoryID, input.Subject, input.Allow); err != nil {
			writeReviewError(writer, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("/v1/supply-chain/review/events", authenticated(http.MethodGet, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		events, err := service.Events(request.Context(), PrincipalFromContext(request.Context()))
		if err != nil {
			writeReviewError(writer, err)
			return
		}
		response := struct {
			Events []api.SupplyChainReviewEvent `json:"events"`
		}{Events: make([]api.SupplyChainReviewEvent, 0, len(events))}
		for _, event := range events {
			response.Events = append(response.Events, api.SupplyChainReviewEvent{ID: event.ID, Kind: event.Kind, Actor: event.Actor, RepositoryID: event.RepositoryID, Target: event.Target, Detail: event.Detail, CreatedAt: event.CreatedAt})
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	})))
	mux.Handle("/v1/supply-chain/policies", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				policies, err := service.Policies(request.Context(), PrincipalFromContext(request.Context()))
				if err != nil {
					writeReviewError(writer, err)
					return
				}
				response := struct {
					Policies []api.SupplyChainPolicy `json:"policies"`
				}{Policies: make([]api.SupplyChainPolicy, 0, len(policies))}
				for _, item := range policies {
					response.Policies = append(response.Policies, policySummary(item))
				}
				writeBoundedJSON(writer, response, maxResponseBytes)
			})).ServeHTTP(writer, request)
		case http.MethodPost:
			AuthenticateRequest(authenticator, jsonSCIPHandler(maxRequestBytes, func(writer http.ResponseWriter, request *http.Request, input api.SupplyChainCreatePolicyRequest) {
				var created policy.Policy
				var err error
				if input.InstallExample {
					created, err = service.InstallExamplePolicy(request.Context(), PrincipalFromContext(request.Context()), input.Activate)
				} else {
					created, err = service.CreatePolicy(request.Context(), PrincipalFromContext(request.Context()), input.Name, input.Description, input.Rules, policy.Verdict(input.UnknownHandling), input.Activate)
				}
				if err != nil {
					writeReviewError(writer, err)
					return
				}
				writeBoundedJSONStatus(writer, http.StatusCreated, policySummary(created), maxResponseBytes)
			})).ServeHTTP(writer, request)
		default:
			writer.Header().Set("Allow", "GET, POST")
			writeError(writer, http.StatusMethodNotAllowed, "invalid_request", "request is invalid", false)
		}
	}))
}

func coordinates(input api.SupplyChainCoordinates) license.Coordinates {
	return license.Coordinates{Ecosystem: input.Ecosystem, Namespace: input.Namespace, Name: input.Name, Version: input.Version}
}

func apiCoordinates(input license.Coordinates) api.SupplyChainCoordinates {
	return api.SupplyChainCoordinates{Ecosystem: input.Ecosystem, Namespace: input.Namespace, Name: input.Name, Version: input.Version}
}

func queueItem(item review.QueueItem) api.SupplyChainReviewItem {
	result := api.SupplyChainReviewItem{ComponentID: item.ComponentID, SnapshotID: item.SnapshotID, RepositoryID: item.RepositoryGitHubID, Repository: item.Repository, ElementID: item.ElementID, Name: item.Name, Version: item.Version, PURL: item.PURL,
		Coordinates: apiCoordinates(item.Coordinates), Assessment: string(item.AssessmentStatus), Expression: item.Expression, Verdict: string(item.Verdict), Explanation: item.Explanation, PolicyID: item.PolicyID, StaleDecisionID: item.StaleDecisionID, Reason: item.Reason}
	if len(item.EvidenceFingerprint) > 0 {
		result.Basis = hex.EncodeToString(item.EvidenceFingerprint)
	}
	return result
}

func conclusionSummary(conclusion review.Conclusion) api.SupplyChainConclusion {
	return api.SupplyChainConclusion{ID: conclusion.ID, Coordinates: apiCoordinates(conclusion.Coordinates), EvidenceID: conclusion.EvidenceID, Basis: hex.EncodeToString(conclusion.BasisFingerprint), Reviewer: conclusion.Reviewer, Reason: conclusion.Reason, CreatedAt: conclusion.CreatedAt, SupersededBy: conclusion.SupersededBy}
}

func decisionSummary(decision review.Decision) api.SupplyChainDecision {
	return api.SupplyChainDecision{ID: decision.ID, Coordinates: apiCoordinates(decision.Coordinates), Kind: string(decision.Kind), PolicyID: decision.PolicyID, PolicyVerdict: string(decision.PolicyVerdict), Basis: hex.EncodeToString(decision.EvidenceFingerprint),
		Reviewer: decision.Reviewer, Reason: decision.Reason, UsageContext: decision.UsageContext, ExpiresAt: decision.ExpiresAt, CreatedAt: decision.CreatedAt, SupersededBy: decision.SupersededBy}
}

func policySummary(item policy.Policy) api.SupplyChainPolicy {
	return api.SupplyChainPolicy{ID: item.ID, Name: item.Name, Version: item.Version, Kind: item.Kind, Active: item.Active, UnknownHandling: string(item.UnknownHandling), Description: item.Description, CreatedBy: item.CreatedBy,
		Approved: nonNil(item.Rules.Approved), ReviewRequired: nonNil(item.Rules.ReviewRequired), Prohibited: nonNil(item.Rules.Prohibited), AllowOrLater: item.Rules.AllowOrLater}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func writeReviewError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, review.ErrForbidden):
		writeError(writer, http.StatusForbidden, "forbidden", "review permission for this repository is required", false)
	case errors.Is(err, review.ErrStaleBasis):
		writeError(writer, http.StatusConflict, "stale_basis", "the evidence changed since it was viewed; reload and review the current evidence", false)
	case errors.Is(err, review.ErrInvalidRequest):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
	case errors.Is(err, review.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "not found", false)
	default:
		writeSupplyChainError(writer, err)
	}
}
