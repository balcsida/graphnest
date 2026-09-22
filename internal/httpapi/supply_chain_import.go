package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/supplychain"
)

// UploadGrants manages repository-scoped import permissions. Resolve maps an
// authorized GitHub repository ID to the internal row for the principal.
type UploadGrants struct {
	Set     func(ctx context.Context, repositoryID int64, subject, grantedBy string, allow bool) error
	Resolve func(ctx context.Context, principal authn.Principal, githubID int64) (int64, error)
}

// RegisterSupplyChainImports mounts the standards-based import route and the
// administrator-only upload-grant route.
//
//	POST /v1/supply-chain/imports?repository_id=101&subject=source&label=ort[&subject_revision=<sha>]
//	Content-Type: application/spdx+json | application/vnd.cyclonedx+json | application/json
func RegisterSupplyChainImports(mux *http.ServeMux, authenticator authn.RequestAuthenticator, importer *supplychain.Importer, grants *UploadGrants, maxUploadBytes, maxResponseBytes int64) {
	mux.Handle("/v1/supply-chain/imports", exactMethod(http.MethodPost, AuthenticateRequest(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
		switch contentType {
		case "application/json", "application/spdx+json", "application/vnd.cyclonedx+json":
		default:
			writeError(writer, http.StatusUnsupportedMediaType, "invalid_request", "request is invalid", false)
			return
		}
		query := request.URL.Query()
		repositoryID, err := strconv.ParseInt(query.Get("repository_id"), 10, 64)
		if err != nil || repositoryID < 1 || len(query["repository_id"]) != 1 || len(query["subject"]) != 1 || len(query["label"]) != 1 {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		controller := http.NewResponseController(writer)
		_ = controller.SetReadDeadline(time.Now().Add(2 * time.Minute))
		data, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxUploadBytes))
		if err != nil {
			writeError(writer, invalidRequestStatus(err), "invalid_request", "request is invalid", false)
			return
		}
		response, err := importer.Import(request.Context(), PrincipalFromContext(request.Context()), supplychain.ImportRequest{
			RepositoryID: repositoryID, Subject: supplychain.Subject(query.Get("subject")), Label: query.Get("label"), SubjectRevision: query.Get("subject_revision"),
			ContentType: contentType, Document: data,
		})
		if err != nil {
			writeSupplyChainImportError(writer, err)
			return
		}
		writeBoundedJSONStatus(writer, http.StatusCreated, response, maxResponseBytes)
	}))))
	if grants == nil {
		return
	}
	mux.Handle("/v1/supply-chain/upload-grants", exactMethod(http.MethodPut, AuthenticateRequest(authenticator, jsonSCIPHandler(4<<10, func(writer http.ResponseWriter, request *http.Request, input struct {
		RepositoryID int64  `json:"repository_id"`
		Subject      string `json:"subject"`
		Allow        bool   `json:"allow"`
	}) {
		principal := PrincipalFromContext(request.Context())
		if !principal.Administrator {
			writeSupplyChainError(writer, supplychain.ErrForbidden)
			return
		}
		if input.RepositoryID < 1 || input.Subject == "" || len(input.Subject) > 256 {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid", false)
			return
		}
		internalID, err := grants.Resolve(request.Context(), principal, input.RepositoryID)
		if err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		if err := grants.Set(request.Context(), internalID, input.Subject, principal.Subject, input.Allow); err != nil {
			writeSupplyChainError(writer, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))))
}

func writeSupplyChainImportError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, supplychain.ErrUnsupportedFormat), errors.Is(err, supplychain.ErrUnsupportedVersion):
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_format", "only SPDX 2.3 JSON and CycloneDX 1.6 JSON documents are accepted", false)
	case errors.Is(err, supplychain.ErrMalformed):
		writeError(writer, http.StatusBadRequest, "malformed_document", "document could not be parsed", false)
	case errors.Is(err, supplychain.ErrTooLarge), errors.Is(err, supplychain.ErrImportTooLarge):
		writeError(writer, http.StatusRequestEntityTooLarge, "too_large", "document exceeds the configured limits", false)
	case errors.Is(err, supplychain.ErrQuotaExceeded):
		writeError(writer, http.StatusTooManyRequests, "quota_exceeded", "import quota for this repository is exhausted", true)
	case errors.Is(err, supplychain.ErrForbidden):
		writeError(writer, http.StatusForbidden, "forbidden", "upload permission for this repository is required", false)
	default:
		writeSupplyChainError(writer, err)
	}
}
