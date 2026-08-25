package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/search"
	"github.com/balcsida/graphnest/internal/zoekt"
	"github.com/balcsida/graphnest/pkg/api"
)

type principalContextKey struct{}

func PrincipalFromContext(ctx context.Context) authn.Principal {
	principal, _ := ctx.Value(principalContextKey{}).(authn.Principal)
	return principal
}

func RegisterSearch(mux *http.ServeMux, authenticator authn.RequestAuthenticator, service *search.Service, maxRequestBytes, maxResponseBytes int64) {
	searchHandler := AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Content-Type") != "application/json" {
			writeError(writer, http.StatusUnsupportedMediaType, "invalid_request", "request is invalid", false)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var input api.SearchRequest
		if err := decoder.Decode(&input); err != nil {
			writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
			return
		}
		response, err := service.Search(request.Context(), PrincipalFromContext(request.Context()), input)
		if err != nil {
			writeSearchError(writer, err)
			return
		}
		writeBoundedJSON(writer, response, maxResponseBytes)
	}))
	mux.Handle("/v1/search", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeError(writer, http.StatusMethodNotAllowed, "invalid_request", "request is invalid", false)
			return
		}
		searchHandler.ServeHTTP(writer, request)
	}))
}

func invalidRequestStatus(err error) int {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func writeSearchError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeError(writer, http.StatusGatewayTimeout, "timeout", "search timed out", true)
	case errors.Is(err, search.ErrInvalidQuery), errors.Is(err, zoekt.ErrInvalidQuery):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
	case errors.Is(err, zoekt.ErrResponseTooLarge):
		writeError(writer, http.StatusBadGateway, "backend_error", "search backend failed", false)
	default:
		writeError(writer, http.StatusServiceUnavailable, "unavailable", "service unavailable", true)
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string, retryable bool) {
	requestID := writer.Header().Get("X-Request-ID")
	if requestID == "" {
		requestID = rand.Text()
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": message, "request_id": requestID, "retryable": retryable,
	}})
}
