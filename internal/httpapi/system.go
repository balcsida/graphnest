package httpapi

import (
	"context"
	"net/http"
)

type ReadyChecker interface {
	Health(context.Context) error
}

func RegisterSystem(mux *http.ServeMux, checker ReadyChecker, metrics http.Handler) {
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = writer.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		if checker != nil && checker.Health(request.Context()) != nil {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte("{\"error\":\"unavailable\"}\n"))
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = writer.Write([]byte("ok\n"))
	})
	mux.Handle("GET /metrics", metrics)
}
