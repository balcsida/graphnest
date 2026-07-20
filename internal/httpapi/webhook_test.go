package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/webhook"
)

type webhookProcessorStub struct {
	calls  int
	result bool
	err    error
	got    webhook.Delivery
}

func (stub *webhookProcessorStub) Process(_ context.Context, delivery webhook.Delivery) (bool, error) {
	stub.calls++
	stub.got = delivery
	return stub.result, stub.err
}

func TestGitHubWebhookRejectsUntrustedRequests(t *testing.T) {
	secret := []byte("webhook-secret")
	body := `{"installation":{"id":1}}`
	validSignature := signWebhook(secret, []byte(body))
	for _, test := range []struct {
		name    string
		body    string
		headers map[string][]string
		status  int
	}{
		{"missing event", body, map[string][]string{"X-GitHub-Delivery": {"one"}, "X-Hub-Signature-256": {validSignature}}, http.StatusBadRequest},
		{"empty event", body, map[string][]string{"X-GitHub-Event": {""}, "X-GitHub-Delivery": {"one"}, "X-Hub-Signature-256": {validSignature}}, http.StatusBadRequest},
		{"duplicate event", body, map[string][]string{"X-GitHub-Event": {"push", "push"}, "X-GitHub-Delivery": {"one"}, "X-Hub-Signature-256": {validSignature}}, http.StatusBadRequest},
		{"missing delivery", body, map[string][]string{"X-GitHub-Event": {"push"}, "X-Hub-Signature-256": {validSignature}}, http.StatusBadRequest},
		{"empty delivery", body, map[string][]string{"X-GitHub-Event": {"push"}, "X-GitHub-Delivery": {""}, "X-Hub-Signature-256": {validSignature}}, http.StatusBadRequest},
		{"duplicate delivery", body, map[string][]string{"X-GitHub-Event": {"push"}, "X-GitHub-Delivery": {"one", "two"}, "X-Hub-Signature-256": {validSignature}}, http.StatusBadRequest},
		{"missing signature", body, map[string][]string{"X-GitHub-Event": {"push"}, "X-GitHub-Delivery": {"one"}}, http.StatusBadRequest},
		{"empty signature", body, map[string][]string{"X-GitHub-Event": {"push"}, "X-GitHub-Delivery": {"one"}, "X-Hub-Signature-256": {""}}, http.StatusBadRequest},
		{"duplicate signature", body, map[string][]string{"X-GitHub-Event": {"push"}, "X-GitHub-Delivery": {"one"}, "X-Hub-Signature-256": {validSignature, validSignature}}, http.StatusBadRequest},
		{"wrong prefix", body, map[string][]string{"X-GitHub-Event": {"push"}, "X-GitHub-Delivery": {"one"}, "X-Hub-Signature-256": {"sha1=bad"}}, http.StatusUnauthorized},
		{"invalid signature", body, map[string][]string{"X-GitHub-Event": {"push"}, "X-GitHub-Delivery": {"one"}, "X-Hub-Signature-256": {"sha256=" + strings.Repeat("0", 64)}}, http.StatusUnauthorized},
		{"too large", strings.Repeat("x", 1024*1024+1), map[string][]string{"X-GitHub-Event": {"push"}, "X-GitHub-Delivery": {"one"}, "X-Hub-Signature-256": {"sha256=bad"}}, http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			processor := &webhookProcessorStub{}
			mux := http.NewServeMux()
			RegisterGitHubWebhook(mux, secret, 1024*1024, processor)
			request := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(test.body))
			for key, values := range test.headers {
				for _, value := range values {
					request.Header.Add(key, value)
				}
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.status || processor.calls != 0 {
				t.Fatalf("status=%d calls=%d", response.Code, processor.calls)
			}
		})
	}
}

func TestGitHubWebhookPassesVerifiedMalformedJSONToProcessor(t *testing.T) {
	secret := []byte("webhook-secret")
	body := []byte("{")
	processor := &webhookProcessorStub{err: webhook.InvalidDeliveryError{}}
	mux := http.NewServeMux()
	RegisterGitHubWebhook(mux, secret, 1024*1024, processor)
	request := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	request.Header.Set("X-GitHub-Event", "push")
	request.Header.Set("X-GitHub-Delivery", "verified-malformed")
	request.Header.Set("X-Hub-Signature-256", signWebhook(secret, body))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || processor.calls != 1 {
		t.Fatalf("status=%d calls=%d", response.Code, processor.calls)
	}
}

func TestGitHubWebhookMapsProcessorErrors(t *testing.T) {
	secret := []byte("webhook-secret")
	body := []byte(`{"installation":{"id":1}}`)
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{"invalid", webhook.InvalidDeliveryError{}, http.StatusBadRequest},
		{"unavailable", errors.New("down"), http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			processor := &webhookProcessorStub{err: test.err}
			mux := http.NewServeMux()
			RegisterGitHubWebhook(mux, secret, 1024*1024, processor)
			request := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
			request.Header.Set("X-GitHub-Event", "installation")
			request.Header.Set("X-GitHub-Delivery", "one")
			request.Header.Set("X-Hub-Signature-256", signWebhook(secret, body))
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.status || processor.calls != 1 {
				t.Fatalf("status=%d calls=%d", response.Code, processor.calls)
			}
		})
	}
}

func TestGitHubWebhookAcceptsVerifiedDelivery(t *testing.T) {
	secret := []byte("webhook-secret")
	body := []byte(`{"installation":{"id":1}}`)
	processor := &webhookProcessorStub{result: true}
	mux := http.NewServeMux()
	RegisterGitHubWebhook(mux, secret, 1024*1024, processor)
	request := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	request.Header.Set("X-GitHub-Event", "installation")
	request.Header.Set("X-GitHub-Delivery", "one")
	request.Header.Set("X-Hub-Signature-256", signWebhook(secret, body))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || processor.calls != 1 {
		t.Fatalf("status=%d calls=%d", response.Code, processor.calls)
	}
}

func TestGitHubWebhookAcceptsExactLimit(t *testing.T) {
	secret := []byte("webhook-secret")
	body := []byte(strings.Repeat(" ", 1024*1024-2) + "{}")
	processor := &webhookProcessorStub{result: true}
	mux := http.NewServeMux()
	RegisterGitHubWebhook(mux, secret, 1024*1024, processor)
	request := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	request.Header.Set("X-GitHub-Event", "ping")
	request.Header.Set("X-GitHub-Delivery", "exact-limit")
	request.Header.Set("X-Hub-Signature-256", signWebhook(secret, body))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || processor.calls != 1 {
		t.Fatalf("status=%d calls=%d", response.Code, processor.calls)
	}
}

func TestGitHubWebhookPassesAcceptedIgnoredAndDuplicateEvents(t *testing.T) {
	secret := []byte("webhook-secret")
	for _, test := range []struct {
		name, event, body string
		result            bool
	}{
		{"unknown", "ping", `{"zen":"keep it logically awesome"}`, false},
		{"duplicate", "push", `{"installation":{"id":1},"repository":{"id":2},"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, false},
		{"installation", "installation", `{"action":"created","installation":{"id":1}}`, true},
		{"repository", "repository", `{"action":"renamed","installation":{"id":1},"repository":{"id":2}}`, true},
		{"push ref", "push", `{"installation":{"id":1},"repository":{"id":2},"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			processor := &webhookProcessorStub{result: test.result}
			mux := http.NewServeMux()
			RegisterGitHubWebhook(mux, secret, 1024*1024, processor)
			request := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(test.body))
			request.Header.Set("X-GitHub-Event", test.event)
			request.Header.Set("X-GitHub-Delivery", "one")
			request.Header.Set("X-Hub-Signature-256", signWebhook(secret, []byte(test.body)))
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusAccepted || processor.calls != 1 || processor.got.Event != test.event || string(processor.got.Body) != test.body {
				t.Fatalf("status=%d calls=%d delivery=%#v", response.Code, processor.calls, processor.got)
			}
		})
	}
}

func signWebhook(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
