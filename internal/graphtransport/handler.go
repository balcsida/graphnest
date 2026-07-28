package graphtransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/grepnest/grepnest/internal/graphprotocol"
)

const (
	defaultRequestBytes  = 64 << 10
	defaultResponseBytes = 256 << 10
	defaultTimeout       = 5 * time.Second
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Limits struct {
	MaxRequestBytes, MaxResponseBytes           int64
	RequestTimeout                              time.Duration
	ContextTimeout, ImpactTimeout, TraceTimeout time.Duration
	CypherTimeout                               time.Duration
}

type healthChecker interface {
	Health(context.Context) error
}

type handler struct {
	engine     graphprotocol.QueryEngine
	secretHash [sha256.Size]byte
	configured bool
	limits     Limits
}

func NewHandler(secret []byte, engine graphprotocol.QueryEngine, limits Limits) http.Handler {
	limits = normalizedLimits(limits)
	return &handler{
		engine: engine, secretHash: sha256.Sum256(secret),
		configured: len(secret) > 0 && engine != nil, limits: limits,
	}
}

func normalizedLimits(limits Limits) Limits {
	if limits.MaxRequestBytes <= 0 {
		limits.MaxRequestBytes = defaultRequestBytes
	}
	if limits.MaxResponseBytes <= 0 {
		limits.MaxResponseBytes = defaultResponseBytes
	}
	if limits.RequestTimeout <= 0 {
		limits.RequestTimeout = defaultTimeout
	}
	return limits
}

func (handler *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/healthz":
		handler.health(writer, request, false)
		return
	case "/readyz":
		handler.health(writer, request, true)
		return
	}
	route, ok := handler.route(request.URL.Path)
	if !ok {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if !handler.configured {
		writeError(writer, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if !handler.authorized(request.Header.Get("Authorization")) {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, http.StatusMethodNotAllowed, "invalid_request")
		return
	}
	if request.Header.Get("Content-Type") != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "invalid_request")
		return
	}
	route(writer, request)
}

func (handler *handler) authorized(header string) bool {
	const prefix = "Bearer "
	candidate := []byte("")
	if strings.HasPrefix(header, prefix) {
		candidate = []byte(strings.TrimPrefix(header, prefix))
	}
	candidateHash := sha256.Sum256(candidate)
	return subtle.ConstantTimeCompare(handler.secretHash[:], candidateHash[:]) == 1
}

func (handler *handler) health(writer http.ResponseWriter, request *http.Request, ready bool) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeError(writer, http.StatusMethodNotAllowed, "invalid_request")
		return
	}
	if ready && (!handler.configured || handler.engineHealth(request.Context()) != nil) {
		writeError(writer, http.StatusServiceUnavailable, "unavailable")
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = writer.Write([]byte("ok\n"))
}

func (handler *handler) engineHealth(ctx context.Context) error {
	if checker, ok := handler.engine.(healthChecker); ok {
		return checker.Health(ctx)
	}
	return nil
}

type routeFunc func(http.ResponseWriter, *http.Request)

func (handler *handler) route(path string) (routeFunc, bool) {
	switch path {
	case "/internal/v1/graph/context":
		return func(writer http.ResponseWriter, request *http.Request) {
			handler.execute(writer, request, handler.timeout(handler.limits.ContextTimeout),
				func(ctx context.Context, decoder *json.Decoder) (any, error) {
					var value graphprotocol.ContextRequest
					if err := decode(decoder, &value); err != nil {
						return nil, err
					}
					if !validScope(value.Scope) {
						return nil, errInvalidRequest
					}
					return handler.engine.Context(ctx, value)
				})
		}, true
	case "/internal/v1/graph/impact":
		return func(writer http.ResponseWriter, request *http.Request) {
			handler.execute(writer, request, handler.timeout(handler.limits.ImpactTimeout),
				func(ctx context.Context, decoder *json.Decoder) (any, error) {
					var value graphprotocol.ImpactRequest
					if err := decode(decoder, &value); err != nil {
						return nil, err
					}
					if !validScope(value.Scope) {
						return nil, errInvalidRequest
					}
					return handler.engine.Impact(ctx, value)
				})
		}, true
	case "/internal/v1/graph/trace":
		return func(writer http.ResponseWriter, request *http.Request) {
			handler.execute(writer, request, handler.timeout(handler.limits.TraceTimeout),
				func(ctx context.Context, decoder *json.Decoder) (any, error) {
					var value graphprotocol.TraceRequest
					if err := decode(decoder, &value); err != nil {
						return nil, err
					}
					if !validScope(value.Scope) {
						return nil, errInvalidRequest
					}
					return handler.engine.Trace(ctx, value)
				})
		}, true
	case "/internal/v1/graph/cypher":
		return func(writer http.ResponseWriter, request *http.Request) {
			handler.execute(writer, request, handler.timeout(handler.limits.CypherTimeout),
				func(ctx context.Context, decoder *json.Decoder) (any, error) {
					var value graphprotocol.CypherRequest
					if err := decode(decoder, &value); err != nil {
						return nil, err
					}
					if len(value.Scope.Repositories) > 0 && !validScope(value.Scope) {
						return nil, errInvalidRequest
					}
					return handler.engine.Cypher(ctx, value)
				})
		}, true
	default:
		return nil, false
	}
}

func (handler *handler) timeout(route time.Duration) time.Duration {
	if route > 0 {
		return route
	}
	return handler.limits.RequestTimeout
}

var errInvalidRequest = errors.New("invalid request")

func decode(decoder *json.Decoder, target any) error {
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errInvalidRequest
	}
	return nil
}

func validScope(scope graphprotocol.Scope) bool {
	if len(scope.Repositories) == 0 {
		return false
	}
	seen := make(map[int64]struct{}, len(scope.Repositories))
	for _, repository := range scope.Repositories {
		if repository.ID <= 0 || !commitPattern.MatchString(repository.Commit) {
			return false
		}
		if _, exists := seen[repository.ID]; exists {
			return false
		}
		seen[repository.ID] = struct{}{}
	}
	return true
}

type operation func(context.Context, *json.Decoder) (any, error)

func (handler *handler) execute(writer http.ResponseWriter, request *http.Request, timeout time.Duration, operation operation) {
	body, err := io.ReadAll(io.LimitReader(request.Body, handler.limits.MaxRequestBytes+1))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	if int64(len(body)) > handler.limits.MaxRequestBytes {
		writeError(writer, http.StatusRequestEntityTooLarge, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), timeout)
	defer cancel()
	value, err := operation(ctx, json.NewDecoder(bytes.NewReader(body)))
	if err != nil {
		status, code := classifyError(err)
		writeError(writer, status, code)
		return
	}
	data, err := json.Marshal(value)
	if err != nil || int64(len(data)+1) > handler.limits.MaxResponseBytes {
		writeCappedError(writer, http.StatusServiceUnavailable, "response_too_large", handler.limits.MaxResponseBytes)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(writer, bytes.NewReader(append(data, '\n')))
}

func classifyError(err error) (int, string) {
	switch {
	case errors.Is(err, errInvalidRequest), err.Error() == "invalid graph query",
		err.Error() == "administrator required":
		return http.StatusBadRequest, "invalid_request"
	case isJSONError(err):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, "unavailable"
	default:
		return http.StatusServiceUnavailable, "unavailable"
	}
}

func isJSONError(err error) bool {
	var syntax *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &typeError) ||
		strings.HasPrefix(err.Error(), "json: unknown field") || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF)
}

func writeError(writer http.ResponseWriter, status int, code string) {
	data := errorBody(code)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(data)
}

func writeCappedError(writer http.ResponseWriter, status int, code string, maxBytes int64) {
	data := errorBody(code)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if int64(len(data)) <= maxBytes {
		_, _ = writer.Write(data)
	}
}

func errorBody(code string) []byte {
	data, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": code, "message": errorMessage(code), "retryable": code == "unavailable",
	}})
	return append(data, '\n')
}

func errorMessage(code string) string {
	switch code {
	case "unauthorized":
		return "authentication required"
	case "not_found":
		return "not found"
	case "unavailable", "response_too_large":
		return "graph service is unavailable"
	default:
		return "request is invalid"
	}
}
