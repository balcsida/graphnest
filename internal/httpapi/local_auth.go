package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/grepnest/grepnest/internal/audit"
	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/sso"
)

// 16 KiB covers three maximally JSON-escaped credential fields without
// allowing an unbounded authentication request.
const localAuthMaxBodyBytes = 16 << 10

type passwordCredentialSetter interface {
	CreatePasswordSession(context.Context, int64, authn.PasswordCredential, authn.SessionRecord, [32]byte, [32]byte) error
	RotatePasswordCredential(context.Context, int64, authn.PasswordCredential, authn.PasswordCredential, authn.SessionRecord, [32]byte, [32]byte, audit.Event) error
}

func RegisterLocalAuth(mux *http.ServeMux, publicOrigin string, authenticator *authn.LocalAuthenticator, credentials passwordCredentialSetter) {
	if authenticator == nil || credentials == nil || publicOrigin == "" {
		return
	}
	mux.Handle("/auth/local", localAuthBoundary(publicOrigin, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			UserName string `json:"user_name"`
			Password string `json:"password"`
		}
		if !decodeLocalAuthJSON(writer, request, &input) {
			return
		}
		password := []byte(input.Password)
		input.Password = ""
		verification, err := authenticator.Verify(request.Context(), input.UserName, password, request.RemoteAddr)
		if err != nil {
			writeLocalAuthenticationError(writer, err)
			return
		}
		defer clear(verification.Credential.Salt)
		defer clear(verification.Credential.Hash)
		if verification.ForceRotation {
			writeLocalUnauthenticated(writer)
			return
		}
		prepared, err := authenticator.Sessions.PrepareForUser(verification.UserID, "local", false)
		if err != nil {
			writeLocalUnauthenticated(writer)
			return
		}
		accountKey, sourceKey := verification.ThrottleKeys()
		if err := credentials.CreatePasswordSession(request.Context(), verification.UserID, verification.Credential, prepared.Record, accountKey, sourceKey); err != nil {
			writeLocalUnauthenticated(writer)
			return
		}
		result, err := authenticator.CompleteLogin(request.Context(), verification, prepared)
		if err != nil {
			writeLocalUnauthenticated(writer)
			return
		}
		http.SetCookie(writer, sso.SessionCookie(result.Token, result.ExpiresAt, time.Time{}))
		writer.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("/auth/local/rotate", localAuthBoundary(publicOrigin, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			UserName        string `json:"user_name"`
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if !decodeLocalAuthJSON(writer, request, &input) {
			return
		}
		currentPassword, newPassword := []byte(input.CurrentPassword), []byte(input.NewPassword)
		input.CurrentPassword, input.NewPassword = "", ""
		defer clear(currentPassword)
		defer clear(newPassword)
		if len(newPassword) < 16 || len(newPassword) > 1024 {
			writeLocalInvalidRequest(writer, http.StatusBadRequest)
			return
		}
		verification, err := authenticator.Verify(request.Context(), input.UserName, currentPassword, request.RemoteAddr)
		if err != nil {
			writeLocalAuthenticationError(writer, err)
			return
		}
		defer clear(verification.Credential.Salt)
		defer clear(verification.Credential.Hash)
		if !verification.ForceRotation {
			writeLocalUnauthenticated(writer)
			return
		}
		credential, err := authn.HashPassword(newPassword, nil)
		if err != nil {
			writeLocalUnavailable(writer)
			return
		}
		defer clear(credential.Salt)
		defer clear(credential.Hash)
		prepared, err := authenticator.Sessions.PrepareForUser(verification.UserID, "local", false)
		if err != nil {
			writeLocalUnavailable(writer)
			return
		}
		userID := strconv.FormatInt(verification.UserID, 10)
		accountKey, sourceKey := verification.ThrottleKeys()
		if err := credentials.RotatePasswordCredential(request.Context(), verification.UserID, verification.Credential, credential, prepared.Record, accountKey, sourceKey, audit.Event{
			ActorType: "user", ActorID: userID, TargetType: "user", TargetID: userID,
			AuthenticationMethod: "local", Operation: "password_rotated", Outcome: "success",
			RequestID: audit.RequestID(request.Context()),
		}); err != nil {
			if errors.Is(err, authn.ErrUnauthenticated) {
				writeLocalUnauthenticated(writer)
				return
			}
			writeLocalUnavailable(writer)
			return
		}
		result, err := authenticator.CompleteRotation(request.Context(), verification, prepared)
		if err != nil {
			writeLocalUnavailable(writer)
			return
		}
		http.SetCookie(writer, sso.SessionCookie(result.Token, result.ExpiresAt, time.Time{}))
		writer.WriteHeader(http.StatusNoContent)
	})))
}

func localAuthBoundary(publicOrigin string, next http.Handler) http.Handler {
	return privateAuth(exactMethod(http.MethodPost, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != "" {
			writeLocalInvalidRequest(writer, http.StatusBadRequest)
			return
		}
		if !exactHeader(request, "Origin", publicOrigin) || !exactHeader(request, "Sec-Fetch-Site", "same-origin") {
			writeLocalUnauthenticated(writer)
			return
		}
		next.ServeHTTP(writer, request)
	})))
}

func decodeLocalAuthJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	if !exactHeader(request, "Content-Type", "application/json") {
		writeLocalInvalidRequest(writer, http.StatusUnsupportedMediaType)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, localAuthMaxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeLocalInvalidRequest(writer, invalidRequestStatus(err))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeLocalInvalidRequest(writer, invalidRequestStatus(err))
		return false
	}
	return true
}

func exactHeader(request *http.Request, name, value string) bool {
	values := request.Header.Values(name)
	return len(values) == 1 && values[0] == value
}

func writeLocalAuthenticationError(writer http.ResponseWriter, err error) {
	var throttled *authn.LoginThrottleError
	if errors.As(err, &throttled) {
		writer.Header().Set("Retry-After", "900")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte("{\"error\":{\"code\":\"rate_limited\",\"message\":\"try again later\",\"request_id\":\"\",\"retryable\":true}}\n"))
		return
	}
	writeLocalUnauthenticated(writer)
}

func writeLocalUnauthenticated(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusUnauthorized)
	_, _ = writer.Write([]byte("{\"error\":{\"code\":\"unauthenticated\",\"message\":\"authentication required\",\"request_id\":\"\",\"retryable\":false}}\n"))
}

func writeLocalInvalidRequest(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte("{\"error\":{\"code\":\"invalid_request\",\"message\":\"request is invalid\",\"request_id\":\"\",\"retryable\":false}}\n"))
}

func writeLocalUnavailable(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusServiceUnavailable)
	_, _ = writer.Write([]byte("{\"error\":{\"code\":\"unavailable\",\"message\":\"service unavailable\",\"request_id\":\"\",\"retryable\":true}}\n"))
}
