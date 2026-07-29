package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"

	"github.com/grepnest/grepnest/internal/audit"
)

func RequestIDs(random io.Reader, next http.Handler) http.Handler {
	if random == nil {
		random = rand.Reader
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if audit.RequestID(request.Context()) != "" {
			next.ServeHTTP(writer, request)
			return
		}
		raw := make([]byte, 16)
		if _, err := io.ReadFull(random, raw); err != nil {
			http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		requestID := hex.EncodeToString(raw)
		next.ServeHTTP(writer, request.WithContext(audit.WithRequestID(request.Context(), requestID)))
	})
}
