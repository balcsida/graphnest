package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMilestone2MetricsRecordFixedLabels(t *testing.T) {
	metrics := New()
	metrics.ObserveGitHub("installations", "success")
	metrics.ObserveWebhook("push", "accepted")
	metrics.SetQueueDepth("queued", 3)
	metrics.ObserveIndexPhase("fetch", "success", 1500*time.Millisecond)

	body := scrape(t, metrics)
	for _, want := range []string{
		`graphnest_github_requests_total{operation="installations",result="success"} 1`,
		`graphnest_webhook_deliveries_total{event="push",result="accepted"} 1`,
		`graphnest_index_queue_depth{state="queued"} 3`,
		`graphnest_index_phase_total{phase="fetch",result="success"} 1`,
		`graphnest_index_phase_duration_seconds_count{phase="fetch",result="success"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestArchiveMetricsUseOnlyFixedLabels(t *testing.T) {
	metrics := New()
	metrics.ObserveArchive("download", "success", time.Second)
	metrics.ObserveArchive("repository-123", strings.Repeat("a", 40), time.Second)
	body := scrape(t, metrics)
	for _, want := range []string{
		`graphnest_archive_operations_total{operation="download",result="success"} 1`,
		`graphnest_archive_operations_total{operation="unknown",result="error"} 1`,
		`graphnest_archive_operation_duration_seconds_count{operation="download",result="success"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestMilestone2MetricsBoundUnknownLabels(t *testing.T) {
	metrics := New()
	metrics.ObserveGitHub("repository-secret", "raw error text")
	metrics.ObserveWebhook("private-event", "delivery-secret")
	metrics.SetQueueDepth("repository-123", 7)
	metrics.ObserveIndexPhase("/private/path", "sha-secret", time.Second)

	body := scrape(t, metrics)
	for _, secret := range []string{"repository-secret", "raw error text", "private-event", "delivery-secret", "repository-123", "/private/path", "sha-secret"} {
		if strings.Contains(body, secret) {
			t.Errorf("metrics expose unbounded label %q:\n%s", secret, body)
		}
	}
	for _, want := range []string{
		`operation="unknown",result="error"`,
		`event="unknown",result="error"`,
		`state="unknown"`,
		`phase="unknown",result="error"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing bounded labels %q:\n%s", want, body)
		}
	}
}

func TestGraphMetricsRecordFixedLabels(t *testing.T) {
	metrics := New()
	metrics.SetGraphQueueDepth("running", 2)
	metrics.ObserveGraphPhase("scan", "success", 1500*time.Millisecond)
	metrics.ObserveGraphPhase("publish", "private-error", time.Second)

	body := scrape(t, metrics)
	for _, want := range []string{
		`graphnest_graph_queue_depth{state="running"} 2`,
		`graphnest_graph_scan_phase_total{phase="scan",result="success"} 1`,
		`graphnest_graph_scan_phase_duration_seconds_count{phase="scan",result="success"} 1`,
		`graphnest_graph_scan_phase_total{phase="publish",result="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestAuthMetricsUseOnlyFixedLabels(t *testing.T) {
	metrics := New()
	metrics.ObserveAuth("oidc", "callback", "denied")
	metrics.ObserveAuth("oauth", "callback", "success")
	metrics.ObserveAuth("subject-ada", "issuer=https://idp.example.test", "reason=token-secret")

	body := scrape(t, metrics)
	for _, want := range []string{
		`graphnest_auth_events_total{event="callback",provider="oidc",result="denied"} 1`,
		`graphnest_auth_events_total{event="callback",provider="oauth",result="success"} 1`,
		`graphnest_auth_events_total{event="unknown",provider="unknown",result="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{"subject-ada", "idp.example.test", "token-secret", "issuer=", "reason="} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metrics expose identity detail %q:\n%s", forbidden, body)
		}
	}
}

func TestGraphMetricsIgnoreInvalidLabels(t *testing.T) {
	metrics := New()
	metrics.SetGraphQueueDepth("repository-secret", 7)
	metrics.ObserveGraphPhase("/private/path", "sha-secret", time.Second)

	body := scrape(t, metrics)
	for _, name := range []string{
		"graphnest_graph_queue_depth",
		"graphnest_graph_scan_phase_total",
		"graphnest_graph_scan_phase_duration_seconds",
	} {
		if strings.Contains(body, name) {
			t.Errorf("invalid graph label emitted %q:\n%s", name, body)
		}
	}
}

func TestGraphQueryMetricsUseFixedLabels(t *testing.T) {
	metrics := New()
	metrics.ObserveGraphQuery("context", "success", time.Second)
	metrics.ObserveGraphQuery("repository-42", "deadbeef", time.Second)

	body := scrape(t, metrics)
	for _, want := range []string{
		`graphnest_graph_query_total{operation="context",result="success"} 1`,
		`graphnest_graph_query_total{operation="unknown",result="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{"repository-42", "deadbeef"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics contain unbounded label %q", forbidden)
		}
	}
}

func TestWrapHTTPRecordsRequests(t *testing.T) {
	metrics := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := metrics.WrapHTTP(mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "graphnest_http_requests_total") {
		t.Fatalf("metrics = %s", recorder.Body.String())
	}
}

func TestWrapHTTPBoundsUnknownMethods(t *testing.T) {
	metrics := New()
	handler := metrics.WrapHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, method := range []string{"CUSTOM-1", "CUSTOM-2", "CUSTOM-3"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/healthz", nil))
	}

	body := scrape(t, metrics)
	if strings.Count(body, `graphnest_http_requests_total{method="unknown",path="unknown",status="200"} 3`) != 1 {
		t.Fatalf("metrics missing one unknown-method series:\n%s", body)
	}
	for _, method := range []string{"CUSTOM-1", "CUSTOM-2", "CUSTOM-3"} {
		if strings.Contains(body, method) {
			t.Fatalf("metrics expose unbounded method %q:\n%s", method, body)
		}
	}
}

func TestWrapHTTPRecordsFirstStatus(t *testing.T) {
	metrics := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.WriteHeader(http.StatusInternalServerError)
	})
	handler := metrics.WrapHTTP(mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "path=\"/missing\",status=\"404\"") {
		t.Fatalf("metrics = %s", recorder.Body.String())
	}
}

func TestResponseWriterUnwraps(t *testing.T) {
	underlying := httptest.NewRecorder()
	wrapped := &responseWriter{ResponseWriter: underlying}
	if wrapped.Unwrap() != underlying {
		t.Fatal("response writer did not unwrap")
	}
}

func TestWrapHTTPBoundsPathLabelsToMatchedPatterns(t *testing.T) {
	metrics := New()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/repositories/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := metrics.WrapHTTP(mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/repositories/repository-secret", nil))
	metrics.WrapHTTP(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/raw-path-secret", nil))

	body := scrape(t, metrics)
	if !strings.Contains(body, `path="/v1/repositories/{id}"`) || !strings.Contains(body, `path="unknown"`) {
		t.Fatalf("metrics missing bounded route labels:\n%s", body)
	}
	for _, secret := range []string{"repository-secret", "raw-path-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("metrics expose raw path %q:\n%s", secret, body)
		}
	}
}

func scrape(t *testing.T, metrics *Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder.Body.String()
}
