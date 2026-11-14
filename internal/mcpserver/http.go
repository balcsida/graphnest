package mcpserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPHandler serves the tools over Streamable HTTP without sessions. A
// session would pin a client to the replica that created it and run every
// later call with the principal of its first request.
func HTTPHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true,
		// A call stops when its client has gone.
		PropagateRequestCancellation: true,
	})
}
