package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type changingAuthenticator struct {
	principal atomic.Pointer[authn.Principal]
}

func (auth *changingAuthenticator) Authenticate(context.Context, string) (authn.Principal, error) {
	return *auth.principal.Load(), nil
}

// The sessionless protocol, and the last one whose clients expect a session.
var protocolVersions = []string{"2026-07-28", "2025-11-25"}

func TestHTTPHandlerAuthorizesEachCallByItsOwnRequest(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			auth := &changingAuthenticator{}
			auth.principal.Store(&authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}})
			server := New(testService(t, &recordingBackend{}), mcpRepositoryService())
			httpServer := httptest.NewServer(httpapi.AuthenticateBearer(auth, HTTPHandler(server)))
			defer httpServer.Close()
			session := connectHTTP(t, httpServer.URL, bearerClient(httpServer.Client(), "secret"), version)

			if got := listedRepositories(t, session); len(got) != 1 {
				t.Fatalf("repositories before the access change = %v", got)
			}
			auth.principal.Store(&authn.Principal{InstallationID: 10})
			if got := listedRepositories(t, session); len(got) != 0 {
				t.Fatalf("a connected client still lists %v after losing access", got)
			}
		})
	}
}

func TestHTTPHandlerSharesNothingBetweenReplicas(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			server := New(testService(t, &recordingBackend{}), mcpRepositoryService())
			replicas := []http.Handler{HTTPHandler(server), HTTPHandler(server)}
			var requests atomic.Int32
			balancer := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				replicas[requests.Add(1)%2].ServeHTTP(writer, request)
			})
			auth := authn.NewStatic(map[string]authn.Principal{"secret": {InstallationID: 10, RepositoryIDs: []int64{101}}})
			httpServer := httptest.NewServer(httpapi.AuthenticateBearer(auth, balancer))
			defer httpServer.Close()
			session := connectHTTP(t, httpServer.URL, bearerClient(httpServer.Client(), "secret"), version)

			for range 4 {
				if got := listedRepositories(t, session); len(got) != 1 {
					t.Fatalf("repositories = %v", got)
				}
			}
		})
	}
}

func TestHTTPHandlerKeepsNoSession(t *testing.T) {
	server := New(testService(t, &recordingBackend{}), mcpRepositoryService())
	handler := HTTPHandler(server)
	post := func(sessionID, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if sessionID != "" {
			request.Header.Set("Mcp-Session-Id", sessionID)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	initialized := post("", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if initialized.Code != http.StatusOK || initialized.Header().Get("Mcp-Session-Id") != "" {
		t.Fatalf("initialize: status %d, Mcp-Session-Id %q, want 200 and none", initialized.Code, initialized.Header().Get("Mcp-Session-Id"))
	}
	// An ID from another replica, or one a caller made up, changes nothing.
	listed := post("someone-elses-session", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "list_repositories") {
		t.Fatalf("tools/list with a foreign session ID: status %d, body %s", listed.Code, listed.Body)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/mcp", nil))
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
			t.Fatalf("%s: status %d, Allow %q, want 405 and POST", method, response.Code, response.Header().Get("Allow"))
		}
	}
}

func connectHTTP(t *testing.T, endpoint string, client *http.Client, protocolVersion string) *mcp.ClientSession {
	t.Helper()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client, DisableStandaloneSSE: true},
		&mcp.ClientSessionOptions{ProtocolVersion: protocolVersion},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func listedRepositories(t *testing.T, session *mcp.ClientSession) []json.RawMessage {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_repositories", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("list_repositories: result=%#v err=%v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Repositories []json.RawMessage `json:"repositories"`
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return output.Repositories
}
