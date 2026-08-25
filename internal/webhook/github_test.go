package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/observability"
)

func TestGitHubProcessorRecordsBoundedDeliveryMetrics(t *testing.T) {
	metrics := observability.New()
	processor := NewGitHubProcessor(nil, nil, metrics)
	_, _ = processor.Process(t.Context(), Delivery{ID: "delivery-secret", Event: "push", Body: []byte(`{}`)})

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	if want := `grepnest_webhook_deliveries_total{event="push",result="error"} 1`; strings.Count(body, want) != 1 {
		t.Fatalf("metrics missing %q:\n%s", want, body)
	}
	if strings.Contains(body, "delivery-secret") {
		t.Fatalf("metrics expose delivery ID:\n%s", body)
	}
}

func TestGitHubProcessorRejectsMalformedKnownEventsBeforeStorage(t *testing.T) {
	processor := NewGitHubProcessor(nil, nil)
	for _, test := range []struct{ event, body string }{
		{"push", `{}`},
		{"push", `{"installation":{"id":1},"repository":{"id":2},"ref":"refs/heads/main","after":"bad"}`},
		{"installation", `{"installation":{"id":0}}`},
		{"installation", `{"action":"created","installation":{"id":1}} {}`},
		{"repository", `{"action":"renamed","installation":{"id":1},"repository":{"id":2}}`},
		{"installation_repositories", `{"action":"removed","installation":{"id":1},"repositories_removed":[]}`},
	} {
		_, err := processor.Process(context.Background(), Delivery{ID: "one", Event: test.event, Body: []byte(test.body)})
		var invalid InvalidDeliveryError
		if !errors.As(err, &invalid) {
			t.Fatalf("%s: err=%v", test.event, err)
		}
	}
}

func TestVerify(t *testing.T) {
	secret := []byte("webhook-secret")
	body := []byte(`{"action":"created"}`)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	signature := "sha256=" + fmtHex(mac.Sum(nil))

	for _, test := range []struct {
		name      string
		signature string
		want      bool
	}{
		{"valid", signature, true},
		{"wrong prefix", "sha1=" + signature[7:], false},
		{"wrong digest", "sha256=" + fmtHex(make([]byte, sha256.Size)), false},
		{"non hexadecimal", "sha256=" + string(make([]byte, sha256.Size*2)), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Verify(secret, body, test.signature); got != test.want {
				t.Fatalf("Verify() = %v, want %v", got, test.want)
			}
		})
	}
}

func fmtHex(value []byte) string {
	const hex = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, byteValue := range value {
		result[index*2] = hex[byteValue>>4]
		result[index*2+1] = hex[byteValue&0x0f]
	}
	return string(result)
}
