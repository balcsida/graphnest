package oauthas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const registrationJSON = `{"redirect_uris":["http://127.0.0.1:5000/cb"]}`

func TestRegistrationAcceptsUnknownFieldsAndExactBodyLimit(t *testing.T) {
	cases := []struct{ name, body string }{
		{name: "unknown field", body: registrationJSON[:len(registrationJSON)-1] + `,"unexpected":true}`},
		{name: "small trailing whitespace", body: registrationJSON + " \n\t"},
		{name: "exact limit whitespace", body: registrationJSON + strings.Repeat(" ", maxFormBytes-len(registrationJSON))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			request := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			if response := h.do(request); response.Code != http.StatusCreated {
				t.Fatalf("status=%d, want %d", response.Code, http.StatusCreated)
			}
		})
	}
}

func TestRegistrationRejectsTrailingAndOversizedBodies(t *testing.T) {
	cases := []struct {
		name, suffix string
		contentLen   int64
	}{
		{name: "oversized whitespace known length", suffix: strings.Repeat(" ", maxFormBytes-len(registrationJSON)+1), contentLen: maxFormBytes + 1},
		{name: "oversized whitespace unknown length", suffix: strings.Repeat(" ", maxFormBytes-len(registrationJSON)+1), contentLen: -1},
		{name: "second JSON", suffix: "{}", contentLen: int64(len(registrationJSON) + 2)},
		{name: "standalone junk", suffix: "x", contentLen: int64(len(registrationJSON) + 1)},
		{name: "trailing json known length", suffix: "{}" + strings.Repeat("x", maxFormBytes), contentLen: int64(len(registrationJSON) + 2 + maxFormBytes)},
		{name: "trailing json unknown length", suffix: "{}" + strings.Repeat("x", maxFormBytes), contentLen: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			request := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(registrationJSON+tc.suffix))
			request.ContentLength = tc.contentLen
			request.Header.Set("Content-Type", "application/json")
			response := h.do(request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want %d", response.Code, http.StatusBadRequest)
			}
			var failure map[string]any
			if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
				t.Fatal(err)
			}
			if failure["error"] != "invalid_client_metadata" {
				t.Fatalf("error=%v, want invalid_client_metadata", failure["error"])
			}
			if len(h.store.clients) != 0 || strings.Contains(strings.Join(h.audit.operations(), ","), OperationClientRegistered) {
				t.Fatal("rejected registration mutated state")
			}
		})
	}
}
