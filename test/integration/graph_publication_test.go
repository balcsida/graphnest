//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphingest"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/pkg/api"
)

const (
	publicationSHA     = "2222222222222222222222222222222222222222"
	publicationNextSHA = "3333333333333333333333333333333333333333"
)

// TestGraphPublicationPolicy proves S1.08 over real API tokens and
// PostgreSQL: publication needs an explicit repository grant on top of read
// access; read-only, wrong-repository, ceiling-limited, expired, and revoked
// credentials fail; retries deduplicate; and stale, concurrent, advancing-SHA,
// and cross-producer publications cannot overwrite the wrong generation.
func TestGraphPublicationPolicy(t *testing.T) {
	h := newPostgresHarness(t)
	internal := map[int64]int64{}
	for _, githubID := range []int64{101, 102, 103} {
		internal[githubID] = h.seedRepository(t, 10, githubID)
		setGraphCommit(t, h, internal[githubID], publicationSHA)
	}
	user := func(name string, administrator bool, readable ...int64) int64 {
		t.Helper()
		var id int64
		if err := h.pool.QueryRow(t.Context(), `insert into users (external_id, user_name, source) values ($1, $1, 'scim') returning id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if administrator {
			if _, err := h.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, id); err != nil {
				t.Fatal(err)
			}
		}
		for _, repository := range readable {
			if _, err := h.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, $2)`, id, repository); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	adminID, publisherID, readerID := user("admin", true), user("publisher", false, 101, 102, 103), user("reader", false, 101)
	tokens := authn.TokenManager{Store: h.store}
	token := func(tokens authn.TokenManager, userID int64, ceiling []int64, expires *time.Time) (int64, string) {
		t.Helper()
		id, plaintext, err := tokens.Create(t.Context(), userID, ceiling, expires)
		if err != nil {
			t.Fatal(err)
		}
		return id, plaintext
	}
	_, admin := token(tokens, adminID, []int64{101, 102, 103}, nil)
	publisherTokenID, publisher := token(tokens, publisherID, nil, nil)
	_, reader := token(tokens, readerID, nil, nil)
	_, ceilinged := token(tokens, publisherID, []int64{102}, nil)
	past := time.Now().Add(-time.Hour)
	_, expired := token(authn.TokenManager{Store: h.store, Now: func() time.Time { return past }}, publisherID, nil, new(past.Add(time.Minute)))

	authorizer := authz.NewPostgres(h.store)
	grants := &httpapi.UploadGrants{Set: h.store.SetGraphPublicationGrant, Resolve: func(ctx context.Context, principal authn.Principal, githubID int64) (int64, error) {
		repo, err := authorizer.AuthorizedRepository(ctx, principal, githubID)
		return repo.ID, err
	}}
	mux := http.NewServeMux()
	httpapi.RegisterGraphIngestion(mux, tokens, &graphingest.Service{Store: h.store, MaxUploadBytes: 1 << 20}, grants, 1<<20, 1<<20)

	send := func(method, target, bearer, contentType string, body io.Reader) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, target, body)
		request.Header.Set("Authorization", "Bearer "+bearer)
		request.Header.Set("Content-Type", contentType)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	publish := func(bearer string, repository int64, commit string, expected int64, extra string, artifact []byte) *httptest.ResponseRecorder {
		t.Helper()
		target := fmt.Sprintf("/v1/graph/uploads?repository_id=%d&commit=%s&expected_generation=%d%s", repository, commit, expected, extra)
		return send(http.MethodPost, target, bearer, "application/vnd.graphnest.graph.v2+protobuf", bytes.NewReader(artifact))
	}
	result := func(response *httptest.ResponseRecorder) api.GraphPublicationResult {
		t.Helper()
		var value api.GraphPublicationResult
		if response.Code != http.StatusOK {
			t.Fatalf("publication=%d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	status := func(bearer string, repository int64) api.GraphPublication {
		t.Helper()
		response := send(http.MethodGet, fmt.Sprintf("/v1/graph/repositories/%d/status", repository), bearer, "", nil)
		var value api.GraphStatus
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || response.Code != http.StatusOK || value.Publication == nil {
			t.Fatalf("status=%d %s err=%v", response.Code, response.Body.String(), err)
		}
		return *value.Publication
	}
	grant := func(repository int64, subject int64, allow bool) {
		t.Helper()
		body := fmt.Sprintf(`{"repository_id":%d,"subject":"%d","allow":%t}`, repository, subject, allow)
		if response := send(http.MethodPut, "/v1/graph/publication-grants", admin, "application/json", strings.NewReader(body)); response.Code != http.StatusNoContent {
			t.Fatalf("grant=%d %s", response.Code, response.Body.String())
		}
	}
	first := publicationArtifact(t, "101", publicationSHA, "first")

	// Read access alone never publishes, and preflight says so.
	if preflight := status(publisher, 101); preflight.Permitted || preflight.ActiveGeneration != nil || preflight.MaxUploadBytes != 1<<20 {
		t.Fatalf("ungranted preflight=%#v", preflight)
	}
	if response := publish(publisher, 101, publicationSHA, 0, "", first); response.Code != http.StatusForbidden {
		t.Fatalf("read access published: %d", response.Code)
	}
	if response := send(http.MethodPut, "/v1/graph/publication-grants", publisher, "application/json", strings.NewReader(`{"repository_id":101,"subject":"1","allow":true}`)); response.Code != http.StatusForbidden {
		t.Fatalf("non-administrator granted: %d", response.Code)
	}
	grant(101, publisherID, true)
	if preflight := status(publisher, 101); !preflight.Permitted {
		t.Fatalf("granted preflight=%#v", preflight)
	}
	for name, response := range map[string]*httptest.ResponseRecorder{
		"read-only user":       publish(reader, 101, publicationSHA, 0, "", first),
		"wrong repository":     publish(publisher, 102, publicationSHA, 0, "", publicationArtifact(t, "102", publicationSHA, "first")),
		"artifact for another": publish(publisher, 101, publicationSHA, 0, "", publicationArtifact(t, "102", publicationSHA, "first")),
		"token ceiling":        publish(ceilinged, 101, publicationSHA, 0, "", first),
		"expired token":        publish(expired, 101, publicationSHA, 0, "", first),
		"malformed artifact":   publish(publisher, 101, publicationSHA, 0, "", []byte("not an artifact")),
	} {
		want := map[string]int{"read-only user": http.StatusForbidden, "wrong repository": http.StatusForbidden, "artifact for another": http.StatusBadRequest,
			"token ceiling": http.StatusNotFound, "expired token": http.StatusUnauthorized, "malformed artifact": http.StatusBadRequest}[name]
		if response.Code != want {
			t.Fatalf("%s=%d want %d: %s", name, response.Code, want, response.Body.String())
		}
	}

	// Publish, then retry after a lost response: same generation, no new row.
	published := result(publish(publisher, 101, publicationSHA, 0, "", first))
	if published.Deduplicated || published.ReplacedGeneration != 0 {
		t.Fatalf("first=%#v", published)
	}
	if retry := result(publish(publisher, 101, publicationSHA, 0, "", first)); !retry.Deduplicated || retry.Generation != published.Generation {
		t.Fatalf("retry=%#v", retry)
	}
	active := status(reader, 101).ActiveGeneration
	if active == nil || active.ID != published.Generation || active.Producer != "codegraph" || active.ContentHash != published.ContentHash || active.SchemaVersion != 2 {
		t.Fatalf("active=%#v", active)
	}
	var publisherColumn string
	if err := h.pool.QueryRow(t.Context(), `select publisher from graph_uploads where id=$1`, published.Generation).Scan(&publisherColumn); err != nil || publisherColumn != fmt.Sprintf("api_token:%d", publisherID) {
		t.Fatalf("publisher=%q err=%v", publisherColumn, err)
	}

	// A stale expectation fails; the current one replaces.
	second := publicationArtifact(t, "101", publicationSHA, "second")
	if response := publish(publisher, 101, publicationSHA, 0, "", second); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "generation_conflict") {
		t.Fatalf("stale=%d %s", response.Code, response.Body.String())
	}
	replaced := result(publish(publisher, 101, publicationSHA, published.Generation, "", second))
	if replaced.ReplacedGeneration != published.Generation {
		t.Fatalf("replacement=%#v", replaced)
	}

	// Concurrent publishers from the same preflight: exactly one wins.
	codes := make(chan int, 2)
	var wait sync.WaitGroup
	for _, label := range []string{"third-a", "third-b"} {
		wait.Go(func() {
			codes <- publish(publisher, 101, publicationSHA, replaced.Generation, "", publicationArtifact(t, "101", publicationSHA, label)).Code
		})
	}
	wait.Wait()
	close(codes)
	outcomes := map[int]int{}
	for code := range codes {
		outcomes[code]++
	}
	if outcomes[http.StatusOK] != 1 || outcomes[http.StatusConflict] != 1 {
		t.Fatalf("concurrent outcomes=%v", outcomes)
	}

	// The v2 generation GraphNest derives from a SCIP upload is a separate slot:
	// it is not the publisher's precondition and never a producer conflict. The
	// v1 generation is a third slot.
	if _, err := h.store.ReplaceGraph(t.Context(), internal[103], postgres.GraphSourceManaged, contractArtifact(internal[103], publicationSHA, false)); err != nil {
		t.Fatal(err)
	}
	if err := h.store.ReplaceSCIP(t.Context(), internal[103], publicationSHA, scipgraph.Upload{IndexerName: "scip-go", Occurrences: []scipgraph.Occurrence{{Path: "a.go", Symbol: "scip-go gomod example.com/acme v1 `example.com/acme`/A#", EndCharacter: 1, PositionEncoding: 1, Roles: 1}}}); err != nil {
		t.Fatal(err)
	}
	grant(103, publisherID, true)
	if derived := status(publisher, 103).ActiveGeneration; derived != nil {
		t.Fatalf("preflight reported the SCIP-derived generation: %#v", derived)
	}
	for103 := publicationArtifact(t, "103", publicationSHA, "codegraph")
	if first103 := result(publish(publisher, 103, publicationSHA, 0, "", for103)); first103.ReplacedGeneration != 0 {
		t.Fatalf("first publication=%#v", first103)
	}
	codegraph := status(publisher, 103).ActiveGeneration
	if codegraph == nil || codegraph.Producer != "codegraph" {
		t.Fatalf("preflight after publication=%#v", codegraph)
	}
	// Another producer's published generation needs explicit replacement.
	other := publicationArtifactFor(t, "103", publicationSHA, "other", "other")
	if response := publish(publisher, 103, publicationSHA, codegraph.ID, "", other); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "producer_conflict") {
		t.Fatalf("producer change=%d %s", response.Code, response.Body.String())
	}
	if takeover := result(publish(publisher, 103, publicationSHA, codegraph.ID, "&replace_producer=true", other)); takeover.ReplacedGeneration != codegraph.ID {
		t.Fatalf("takeover=%#v", takeover)
	}
	// A later SCIP upload leaves the published generation alone.
	if err := h.store.ReplaceSCIP(t.Context(), internal[103], publicationSHA, scipgraph.Upload{IndexerName: "scip-go", Occurrences: []scipgraph.Occurrence{{Path: "b.go", Symbol: "scip-go gomod example.com/acme v1 `example.com/acme`/B#", EndCharacter: 1, PositionEncoding: 1, Roles: 1}}}); err != nil {
		t.Fatal(err)
	}
	if after := status(publisher, 103).ActiveGeneration; after == nil || after.Producer != "other" {
		t.Fatalf("SCIP upload replaced the publisher's generation: %#v", after)
	}

	// An advancing indexed SHA rejects the old commit before the body is read.
	setGraphCommit(t, h, internal[101], publicationNextSHA)
	if response := publish(publisher, 101, publicationSHA, 0, "", first); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "not_indexed") {
		t.Fatalf("advanced SHA=%d %s", response.Code, response.Body.String())
	}
	setGraphCommit(t, h, internal[101], publicationSHA)

	// Revocation during the upload is caught by the final recheck: first the
	// credential itself, then the publication grant.
	activeBefore := status(reader, 101).ActiveGeneration.ID
	target := fmt.Sprintf("/v1/graph/uploads?repository_id=101&commit=%s&expected_generation=%d", publicationSHA, activeBefore)
	revokedToken := &revokingReader{Reader: bytes.NewReader(publicationArtifact(t, "101", publicationSHA, "revoked")), revoke: func() {
		if err := h.store.RevokeAPIToken(context.Background(), publisherID, publisherTokenID); err != nil {
			t.Error(err)
		}
	}}
	if response := send(http.MethodPost, target, publisher, "application/vnd.graphnest.graph.v2+protobuf", revokedToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("token revoked mid-call=%d %s", response.Code, response.Body.String())
	}
	_, publisher = token(tokens, publisherID, nil, nil)
	revokedGrant := &revokingReader{Reader: bytes.NewReader(publicationArtifact(t, "101", publicationSHA, "revoked")), revoke: func() {
		grant(101, publisherID, false)
	}}
	if response := send(http.MethodPost, target, publisher, "application/vnd.graphnest.graph.v2+protobuf", revokedGrant); response.Code != http.StatusForbidden {
		t.Fatalf("grant revoked mid-call=%d %s", response.Code, response.Body.String())
	}
	if after := status(reader, 101).ActiveGeneration.ID; after != activeBefore {
		t.Fatalf("revoked publisher changed the active generation: %d -> %d", activeBefore, after)
	}
}

// revokingReader revokes authority once the handler has started reading the
// body, after the pre-read authorization passed.
type revokingReader struct {
	*bytes.Reader
	revoke func()
	once   sync.Once
}

func (reader *revokingReader) Read(data []byte) (int, error) {
	reader.once.Do(reader.revoke)
	return reader.Reader.Read(data)
}

func publicationArtifact(t *testing.T, repository, commit, label string) []byte {
	t.Helper()
	return publicationArtifactFor(t, repository, commit, label, "codegraph")
}

func publicationArtifactFor(t *testing.T, repository, commit, label, producer string) []byte {
	t.Helper()
	data, err := graphartifact.MarshalV2(&graphv2.Artifact{SchemaVersion: 2, Repository: repository, Commit: commit,
		Producer: &graphv2.Producer{Name: producer, Version: "0.7.0", Configuration: "portable"},
		Nodes:    []*graphv2.Node{{SourceId: "a", Occurrence: "declaration:1", Kind: "function", Name: label}, {SourceId: "b", Occurrence: "declaration:2", Kind: "class"}}}, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
