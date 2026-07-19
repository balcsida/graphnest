package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/grepnest/grepnest/internal/webhook"
)

func RegisterGitHubWebhook(mux *http.ServeMux, secret []byte, maxBytes int64, processor webhook.Processor) {
	if maxBytes > 1024*1024 {
		maxBytes = 1024 * 1024
	}
	mux.Handle("POST /v1/webhooks/github", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		event, ok := singleHeader(request, "X-GitHub-Event")
		if !ok {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		delivery, ok := singleHeader(request, "X-GitHub-Delivery")
		if !ok {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		signature, ok := singleHeader(request, "X-Hub-Signature-256")
		if !ok {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(writer, "request too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		if !webhook.Verify(secret, body, signature) {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !json.Valid(body) {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		if _, err := processor.Process(request.Context(), webhook.Delivery{ID: delivery, Event: event, Body: body}); err != nil {
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
}

func singleHeader(request *http.Request, name string) (string, bool) {
	values := request.Header.Values(name)
	return request.Header.Get(name), len(values) == 1 && values[0] != ""
}
