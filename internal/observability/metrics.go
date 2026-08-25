package observability

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	archiveOperations  *prometheus.CounterVec
	archiveDuration    *prometheus.HistogramVec
	registry           *prometheus.Registry
	activeRequests     prometheus.Gauge
	httpRequests       *prometheus.CounterVec
	httpDuration       *prometheus.HistogramVec
	httpResponseSize   *prometheus.HistogramVec
	backendCalls       *prometheus.CounterVec
	backendDuration    *prometheus.HistogramVec
	githubRequests     *prometheus.CounterVec
	webhookDeliveries  *prometheus.CounterVec
	indexQueueDepth    *prometheus.GaugeVec
	indexPhases        *prometheus.CounterVec
	indexDuration      *prometheus.HistogramVec
	graphQueueDepth    *prometheus.GaugeVec
	graphPhases        *prometheus.CounterVec
	graphDuration      *prometheus.HistogramVec
	graphQueries       *prometheus.CounterVec
	graphQueryDuration *prometheus.HistogramVec
	authEvents         *prometheus.CounterVec
}

func New() *Metrics {
	metrics := &Metrics{registry: prometheus.NewRegistry()}
	metrics.archiveOperations = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "graphnest_archive_operations_total", Help: "Archive operations by phase and result."}, []string{"operation", "result"})
	metrics.archiveDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "graphnest_archive_operation_duration_seconds", Help: "Archive operation duration by phase and result."}, []string{"operation", "result"})
	metrics.activeRequests = prometheus.NewGauge(prometheus.GaugeOpts{Name: "graphnest_http_active_requests", Help: "Active HTTP requests."})
	metrics.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "graphnest_http_requests_total", Help: "HTTP requests."}, []string{"method", "path", "status"})
	metrics.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "graphnest_http_request_duration_seconds", Help: "HTTP request duration."}, []string{"method", "path", "status"})
	metrics.httpResponseSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "graphnest_http_response_size_bytes", Help: "HTTP response size."}, []string{"method", "path", "status"})
	metrics.backendCalls = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "graphnest_search_backend_calls_total", Help: "Search backend calls."}, []string{"result"})
	metrics.backendDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "graphnest_search_backend_duration_seconds", Help: "Search backend duration."}, []string{"result"})
	metrics.githubRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "graphnest_github_requests_total", Help: "GitHub API requests."}, []string{"operation", "result"})
	metrics.webhookDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "graphnest_webhook_deliveries_total", Help: "GitHub webhook deliveries."}, []string{"event", "result"})
	metrics.indexQueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "graphnest_index_queue_depth", Help: "Index queue jobs."}, []string{"state"})
	metrics.indexPhases = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "graphnest_index_phase_total", Help: "Index phase executions."}, []string{"phase", "result"})
	metrics.indexDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "graphnest_index_phase_duration_seconds", Help: "Index phase duration."}, []string{"phase", "result"})
	metrics.graphQueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "graphnest_graph_queue_depth", Help: "Graph scan queue jobs."}, []string{"state"})
	metrics.graphPhases = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "graphnest_graph_scan_phase_total", Help: "Graph scan phase executions."}, []string{"phase", "result"})
	metrics.graphDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "graphnest_graph_scan_phase_duration_seconds", Help: "Graph scan phase duration."}, []string{"phase", "result"})
	metrics.graphQueries = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "graphnest_graph_query_total", Help: "Graph queries."}, []string{"operation", "result"})
	metrics.graphQueryDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "graphnest_graph_query_duration_seconds", Help: "Graph query duration."}, []string{"operation", "result"})
	metrics.authEvents = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "graphnest_auth_events_total", Help: "Authentication events."}, []string{"provider", "event", "result"})
	metrics.registry.MustRegister(metrics.archiveOperations, metrics.archiveDuration, metrics.activeRequests, metrics.httpRequests, metrics.httpDuration, metrics.httpResponseSize, metrics.backendCalls, metrics.backendDuration, metrics.githubRequests, metrics.webhookDeliveries, metrics.indexQueueDepth, metrics.indexPhases, metrics.indexDuration, metrics.graphQueueDepth, metrics.graphPhases, metrics.graphDuration, metrics.graphQueries, metrics.graphQueryDuration, metrics.authEvents)
	return metrics
}

func (metrics *Metrics) ObserveGraphQuery(operation, result string, duration time.Duration) {
	labels := []string{fixed(operation, "context", "impact", "trace"), successOrError(result)}
	metrics.graphQueries.WithLabelValues(labels...).Inc()
	metrics.graphQueryDuration.WithLabelValues(labels...).Observe(duration.Seconds())
}

func (metrics *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(metrics.registry, promhttp.HandlerOpts{})
}

func (metrics *Metrics) WrapHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		metrics.activeRequests.Inc()
		defer metrics.activeRequests.Dec()
		started := time.Now()
		recorded := &responseWriter{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(recorded, request)
		labels := []string{fixed(request.Method, http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions), routePattern(request), strconv.Itoa(recorded.status)}
		metrics.httpRequests.WithLabelValues(labels...).Inc()
		metrics.httpDuration.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
		metrics.httpResponseSize.WithLabelValues(labels...).Observe(float64(recorded.bytes))
	})
}

func routePattern(request *http.Request) string {
	pattern := request.Pattern
	if _, path, ok := strings.Cut(pattern, " "); ok {
		pattern = path
	}
	if pattern == "" {
		return "unknown"
	}
	return pattern
}

func (metrics *Metrics) ObserveBackend(duration time.Duration, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	metrics.backendCalls.WithLabelValues(result).Inc()
	metrics.backendDuration.WithLabelValues(result).Observe(duration.Seconds())
}

func (metrics *Metrics) ObserveGitHub(operation, result string) {
	metrics.githubRequests.WithLabelValues(fixed(operation, "installation_token", "installations", "repositories", "default_branch", "contents", "dependency_sbom"), successOrError(result)).Inc()
}

func (metrics *Metrics) ObserveWebhook(event, result string) {
	metrics.webhookDeliveries.WithLabelValues(fixed(event, "push", "installation", "installation_repositories", "repository"), webhookResult(result)).Inc()
}

func (metrics *Metrics) SetQueueDepth(state string, depth int64) {
	metrics.indexQueueDepth.WithLabelValues(fixed(state, "queued", "running", "succeeded", "failed", "superseded")).Set(float64(depth))
}

func (metrics *Metrics) ObserveIndexPhase(phase, result string, duration time.Duration) {
	labels := []string{fixed(phase, "fetch", "index", "visibility"), successOrError(result)}
	metrics.indexPhases.WithLabelValues(labels...).Inc()
	metrics.indexDuration.WithLabelValues(labels...).Observe(duration.Seconds())
}

func (metrics *Metrics) SetGraphQueueDepth(state string, depth int64) {
	state = fixed(state, "queued", "running", "succeeded", "failed", "superseded")
	if state == "unknown" {
		return
	}
	metrics.graphQueueDepth.WithLabelValues(state).Set(float64(depth))
}

func (metrics *Metrics) ObserveGraphPhase(phase, result string, duration time.Duration) {
	phase = fixed(phase, "token", "checkout", "scan", "publish")
	if phase == "unknown" {
		return
	}
	labels := []string{phase, successOrError(result)}
	metrics.graphPhases.WithLabelValues(labels...).Inc()
	metrics.graphDuration.WithLabelValues(labels...).Observe(duration.Seconds())
}

func (metrics *Metrics) ObserveArchive(operation, result string, duration time.Duration) {
	labels := []string{fixed(operation, "download", "extract", "cleanup", "stale_cleanup"), successOrError(result)}
	metrics.archiveOperations.WithLabelValues(labels...).Inc()
	metrics.archiveDuration.WithLabelValues(labels...).Observe(duration.Seconds())
}

func (metrics *Metrics) ObserveAuth(provider, event, result string) {
	metrics.authEvents.WithLabelValues(fixed(provider, "oidc", "oauth", "session", "static"), fixed(event, "login_start", "callback", "session_auth", "logout", "cleanup"), authResult(result)).Inc()
}

func authResult(result string) string {
	for _, candidate := range []string{"success", "invalid", "denied", "error"} {
		if result == candidate {
			return result
		}
	}
	return "error"
}

func fixed(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "unknown"
}

func successOrError(result string) string {
	if result == "success" {
		return result
	}
	return "error"
}

func webhookResult(result string) string {
	for _, candidate := range []string{"accepted", "ignored", "duplicate", "error"} {
		if result == candidate {
			return result
		}
	}
	return "error"
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (writer *responseWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func (writer *responseWriter) WriteHeader(status int) {
	if writer.wrote {
		return
	}
	writer.wrote = true
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *responseWriter) Write(body []byte) (int, error) {
	if !writer.wrote {
		writer.WriteHeader(http.StatusOK)
	}
	count, err := writer.ResponseWriter.Write(body)
	writer.bytes += count
	return count, err
}
