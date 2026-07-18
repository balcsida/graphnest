package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry         *prometheus.Registry
	activeRequests   prometheus.Gauge
	httpRequests     *prometheus.CounterVec
	httpDuration     *prometheus.HistogramVec
	httpResponseSize *prometheus.HistogramVec
	backendCalls     *prometheus.CounterVec
	backendDuration  *prometheus.HistogramVec
}

func New() *Metrics {
	metrics := &Metrics{registry: prometheus.NewRegistry()}
	metrics.activeRequests = prometheus.NewGauge(prometheus.GaugeOpts{Name: "grepnest_http_active_requests", Help: "Active HTTP requests."})
	metrics.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "grepnest_http_requests_total", Help: "HTTP requests."}, []string{"method", "path", "status"})
	metrics.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "grepnest_http_request_duration_seconds", Help: "HTTP request duration."}, []string{"method", "path", "status"})
	metrics.httpResponseSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "grepnest_http_response_size_bytes", Help: "HTTP response size."}, []string{"method", "path", "status"})
	metrics.backendCalls = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "grepnest_search_backend_calls_total", Help: "Search backend calls."}, []string{"result"})
	metrics.backendDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "grepnest_search_backend_duration_seconds", Help: "Search backend duration."}, []string{"result"})
	metrics.registry.MustRegister(metrics.activeRequests, metrics.httpRequests, metrics.httpDuration, metrics.httpResponseSize, metrics.backendCalls, metrics.backendDuration)
	return metrics
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
		labels := []string{request.Method, request.URL.Path, strconv.Itoa(recorded.status)}
		metrics.httpRequests.WithLabelValues(labels...).Inc()
		metrics.httpDuration.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
		metrics.httpResponseSize.WithLabelValues(labels...).Observe(float64(recorded.bytes))
	})
}

func (metrics *Metrics) ObserveBackend(duration time.Duration, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	metrics.backendCalls.WithLabelValues(result).Inc()
	metrics.backendDuration.WithLabelValues(result).Observe(duration.Seconds())
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

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

var defaultMetrics = New()

func WrapHTTP(next http.Handler) http.Handler { return defaultMetrics.WrapHTTP(next) }
