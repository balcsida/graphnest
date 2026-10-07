package mcpserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
)

// Each graph readiness failure tells the agent what is wrong and what fixes
// it; the old single "graph is not ready" hid which one happened.
func TestGraphErrorDistinguishesReadinessFailures(t *testing.T) {
	commit := strings.Repeat("c", 40)
	for _, test := range []struct {
		err  error
		want []string
	}{
		{graphservice.ErrNotIndexed, []string{"not indexed yet", "wait for indexing"}},
		{&graphquery.MissingGenerationError{Commit: commit}, []string{"no graph generation is active", commit, "upload a SCIP index", "publish a graph artifact"}},
		{graphquery.ErrGenerationChanged, []string{"changed during the request", "retry"}},
		{graphquery.ErrDiscoveryUnavailable, []string{"discovery projection is unavailable", "rebuild"}},
		{graphservice.ErrGraphNotReady, []string{"graph is not ready"}},
		{errors.New("PostgreSQL password=secret"), []string{"graph service is unavailable"}},
	} {
		message := graphError(test.err).Error()
		for _, fragment := range test.want {
			if !strings.Contains(message, fragment) {
				t.Fatalf("%v -> %q lacks %q", test.err, message, fragment)
			}
		}
		if strings.Contains(message, "secret") {
			t.Fatalf("leaked internal error: %q", message)
		}
	}
}
