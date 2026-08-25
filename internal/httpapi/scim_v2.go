package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/scim"
)

const (
	scimMaxBodyBytes  = 1 << 20
	scimMaxQueryBytes = 8 << 10
	scimMaxURLBytes   = 16 << 10
)

func GuardSCIMV2(next http.Handler, authenticator authn.ProvisioningAuthenticator, service *scim.Service) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		escapedPath := request.URL.EscapedPath()
		decodedPath := request.URL.Path
		if decodedPath != "/scim/v2" && !strings.HasPrefix(decodedPath, "/scim/v2/") {
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/scim+json")
		if err := authenticator.Authenticate(request.Header.Values("Authorization")); err != nil {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeSCIMError(writer, scim.Error{Status: http.StatusUnauthorized, Detail: "authentication required"})
			return
		}
		if strings.Contains(escapedPath, "%") || escapedPath != request.URL.Path || path.Clean(escapedPath) != escapedPath {
			writeSCIMError(writer, scim.Error{Status: http.StatusNotFound, Detail: "resource not found"})
			return
		}
		serveSCIMV2(writer, request, service)
	})
}

func serveSCIMV2(writer http.ResponseWriter, request *http.Request, service *scim.Service) {
	if len(request.URL.RequestURI()) > scimMaxURLBytes {
		writeSCIMError(writer, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "request URL is too long"})
		return
	}
	if len(request.URL.RawQuery) > scimMaxQueryBytes {
		writeSCIMError(writer, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "query is too long"})
		return
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/scim/v2/"), "/")
	if len(parts) == 0 || len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		writeSCIMError(writer, scim.Error{Status: 404, Detail: "resource not found"})
		return
	}
	if parts[0] == "Users" || parts[0] == "Groups" {
		serveSCIMResource(writer, request, service, parts)
		return
	}
	serveSCIMDiscovery(writer, request, service, parts)
}

func serveSCIMDiscovery(writer http.ResponseWriter, request *http.Request, service *scim.Service, parts []string) {
	var document any
	switch parts[0] {
	case "ServiceProviderConfig":
		if len(parts) != 1 {
			break
		}
		document = scim.ServiceProviderConfig(service.MaxResults)
	case "ResourceTypes":
		if len(parts) == 1 {
			document = scim.ResourceTypes()
		} else if value, ok := scim.ResourceTypeByID(parts[1]); ok {
			document = value
		}
	case "Schemas":
		if len(parts) == 1 {
			document = scim.Schemas()
		} else if value, ok := scim.Schema(parts[1]); ok {
			document = value
		}
	}
	if document == nil {
		writeSCIMError(writer, scim.Error{Status: 404, Detail: "resource not found"})
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeSCIMError(writer, scim.Error{Status: 405, Detail: "method not allowed"})
		return
	}
	if request.URL.RawQuery != "" {
		writeSCIMError(writer, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "query is not supported"})
		return
	}
	writeSCIMJSON(writer, 200, document)
}

func serveSCIMResource(writer http.ResponseWriter, request *http.Request, service *scim.Service, parts []string) {
	resource := scim.ResourceType(parts[0])
	if len(parts) == 1 {
		switch request.Method {
		case http.MethodGet:
			serveSCIMList(writer, request, service, resource)
		case http.MethodPost:
			serveSCIMCreate(writer, request, service, resource)
		default:
			writer.Header().Set("Allow", "GET, POST")
			writeSCIMError(writer, scim.Error{Status: 405, Detail: "method not allowed"})
		}
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodPut &&
		request.Method != http.MethodPatch && request.Method != http.MethodDelete {
		writer.Header().Set("Allow", "GET, PUT, PATCH, DELETE")
		writeSCIMError(writer, scim.Error{Status: 405, Detail: "method not allowed"})
		return
	}
	id, err := parseSCIMID(parts[1])
	if err != nil {
		writeSCIMError(writer, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "resource ID is invalid"})
		return
	}
	switch request.Method {
	case http.MethodGet:
		serveSCIMGet(writer, request, service, resource, id)
	case http.MethodPut:
		serveSCIMReplace(writer, request, service, resource, id)
	case http.MethodPatch:
		serveSCIMPatch(writer, request, service, resource, id)
	case http.MethodDelete:
		if request.URL.RawQuery != "" {
			writeSCIMError(writer, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "query is not supported"})
			return
		}
		var err error
		if resource == scim.ResourceUsers {
			err = service.DeleteUser(request.Context(), id)
		} else {
			err = service.DeleteGroup(request.Context(), id)
		}
		if writeSCIMServiceError(writer, err) {
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}

func serveSCIMList(writer http.ResponseWriter, request *http.Request, service *scim.Service, resource scim.ResourceType) {
	values, err := parseSCIMQuery(request.URL.RawQuery, true)
	if err != nil {
		writeSCIMServiceError(writer, err)
		return
	}
	filter := scim.Filter{}
	if raw := values.Get("filter"); raw != "" {
		filter, err = scim.ParseFilter(resource, raw)
	}
	var page scim.Page
	if err == nil {
		page, err = scim.ParsePage(values, service.MaxResults)
	}
	var projection scim.Projection
	if err == nil {
		projection, err = scim.ParseProjection(values, resource)
	}
	if err != nil {
		writeSCIMParseError(writer, err)
		return
	}
	var document any
	if resource == scim.ResourceUsers {
		document, err = service.Users(request.Context(), filter, page, projection)
	} else {
		document, err = service.Groups(request.Context(), filter, page, projection)
	}
	if !writeSCIMServiceError(writer, err) {
		writeSCIMJSON(writer, 200, document)
	}
}

func serveSCIMGet(writer http.ResponseWriter, request *http.Request, service *scim.Service, resource scim.ResourceType, id int64) {
	values, err := parseSCIMQuery(request.URL.RawQuery, false)
	if err != nil {
		writeSCIMServiceError(writer, err)
		return
	}
	projection, err := scim.ParseProjection(values, resource)
	if err != nil {
		writeSCIMParseError(writer, err)
		return
	}
	var document any
	if resource == scim.ResourceUsers {
		document, err = service.User(request.Context(), id, projection)
	} else {
		document, err = service.Group(request.Context(), id, projection)
	}
	if !writeSCIMServiceError(writer, err) {
		writeSCIMJSON(writer, 200, document)
	}
}

func serveSCIMCreate(writer http.ResponseWriter, request *http.Request, service *scim.Service, resource scim.ResourceType) {
	if request.URL.RawQuery != "" {
		writeSCIMError(writer, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "query is not supported"})
		return
	}
	var document any
	var err error
	if resource == scim.ResourceUsers {
		var input scim.User
		err = decodeSCIM(writer, request, &input)
		if err == nil {
			document, err = service.CreateUser(request.Context(), input)
		}
	} else {
		var input scim.Group
		err = decodeSCIM(writer, request, &input)
		if err == nil {
			document, err = service.CreateGroup(request.Context(), input)
		}
	}
	if writeSCIMServiceError(writer, err) {
		return
	}
	location := scimLocation(document)
	writer.Header().Set("Location", location)
	writeSCIMJSON(writer, http.StatusCreated, document)
}

func serveSCIMReplace(writer http.ResponseWriter, request *http.Request, service *scim.Service, resource scim.ResourceType, id int64) {
	if request.URL.RawQuery != "" {
		writeSCIMError(writer, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "query is not supported"})
		return
	}
	var document any
	var err error
	if resource == scim.ResourceUsers {
		var input scim.User
		err = decodeSCIM(writer, request, &input)
		if err == nil {
			document, err = service.ReplaceUser(request.Context(), id, input)
		}
	} else {
		var input scim.Group
		err = decodeSCIM(writer, request, &input)
		if err == nil {
			document, err = service.ReplaceGroup(request.Context(), id, input)
		}
	}
	if !writeSCIMServiceError(writer, err) {
		writeSCIMJSON(writer, 200, document)
	}
}

func serveSCIMPatch(writer http.ResponseWriter, request *http.Request, service *scim.Service, resource scim.ResourceType, id int64) {
	if request.URL.RawQuery != "" {
		writeSCIMError(writer, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "query is not supported"})
		return
	}
	var input scim.PatchRequest
	if err := decodeSCIM(writer, request, &input); err != nil {
		writeSCIMServiceError(writer, err)
		return
	}
	var document any
	var err error
	if resource == scim.ResourceUsers {
		document, err = service.PatchUser(request.Context(), id, input)
	} else {
		document, err = service.PatchGroup(request.Context(), id, input)
	}
	if !writeSCIMServiceError(writer, err) {
		writeSCIMJSON(writer, 200, document)
	}
}

func parseSCIMQuery(raw string, list bool) (url.Values, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "query is invalid"}
	}
	allowed := map[string]bool{"attributes": true, "excludedAttributes": true}
	if list {
		allowed["filter"], allowed["startIndex"], allowed["count"] = true, true, true
	}
	for name := range values {
		if !allowed[name] {
			return nil, scim.Error{Status: 400, SCIMType: "invalidValue", Detail: "query parameter is unsupported"}
		}
	}
	if filter, ok := values["filter"]; ok && (len(filter) != 1 || filter[0] == "") {
		return nil, scim.Error{Status: 400, SCIMType: "invalidFilter", Detail: "filter is invalid"}
	}
	return values, nil
}

func decodeSCIM(writer http.ResponseWriter, request *http.Request, target any) error {
	if request.Header.Get("Content-Type") != "application/scim+json" {
		return scim.Error{Status: 415, Detail: "Content-Type must be application/scim+json"}
	}
	request.Body = http.MaxBytesReader(writer, request.Body, scimMaxBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return scim.Error{Status: 413, Detail: "request body is too large"}
		}
		return scim.Error{Status: 400, SCIMType: "invalidSyntax", Detail: "request body is invalid"}
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return scim.Error{Status: 400, SCIMType: "invalidSyntax", Detail: "request body is invalid"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return scim.Error{Status: 400, SCIMType: "invalidSyntax", Detail: "request body is invalid"}
	}
	return nil
}

func writeSCIMParseError(writer http.ResponseWriter, err error) {
	if response, ok := scim.ParseRequestError(err); ok {
		writeSCIMError(writer, response)
		return
	}
	writeSCIMServiceError(writer, err)
}

func parseSCIMID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != raw {
		return 0, errors.New("invalid ID")
	}
	return id, nil
}

func scimLocation(document any) string {
	switch value := document.(type) {
	case scim.User:
		return value.Meta.Location
	case scim.Group:
		return value.Meta.Location
	default:
		return ""
	}
}

func writeSCIMServiceError(writer http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var response scim.Error
	if !errors.As(err, &response) {
		response = scim.Error{Status: 500, Detail: "internal server error"}
	}
	writeSCIMError(writer, response)
	return true
}

func writeSCIMError(writer http.ResponseWriter, response scim.Error) {
	if response.Status < 400 || response.Status > 599 {
		response = scim.Error{Status: 500, Detail: "internal server error"}
	}
	writeSCIMJSON(writer, response.Status, response)
}

func writeSCIMJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/scim+json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
