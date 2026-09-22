package license

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// memoryStore is an in-memory Store for worker tests.
type memoryStore struct {
	mu          sync.Mutex
	evidence    []Evidence
	jobs        []*Job
	assessments map[int64]Assessment
	occurrences map[Coordinates][][2]int64
	declared    map[int64]*string
	coordinates []Coordinates
}

func newMemoryStore() *memoryStore {
	return &memoryStore{assessments: map[int64]Assessment{}, occurrences: map[Coordinates][][2]int64{}, declared: map[int64]*string{}}
}

func (store *memoryStore) InsertLicenseEvidence(_ context.Context, evidence Evidence) (int64, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	evidence.ID = int64(len(store.evidence) + 1)
	store.evidence = append(store.evidence, evidence)
	return evidence.ID, false, nil
}

func (store *memoryStore) LatestLicenseEvidence(_ context.Context, coordinates Coordinates) ([]Evidence, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	latest := map[string]Evidence{}
	resolved := map[string]Evidence{}
	for _, evidence := range store.evidence {
		if evidence.Coordinates == coordinates {
			key := string(evidence.Source) + "|" + evidence.Route
			latest[key] = evidence
			if evidence.Outcome == OutcomeResolved {
				resolved[key] = evidence
			}
		}
	}
	var result []Evidence
	for key, evidence := range latest {
		result = append(result, evidence)
		if evidence.Outcome != OutcomeResolved {
			if earlier, ok := resolved[key]; ok {
				result = append(result, earlier)
			}
		}
	}
	return result, nil
}

func (store *memoryStore) EnqueueEnrichment(_ context.Context, coordinates Coordinates, route string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, job := range store.jobs {
		if job.Coordinates == coordinates && job.Route == route && (job.State == "queued" || job.State == "running") {
			return false, nil
		}
	}
	store.jobs = append(store.jobs, &Job{ID: int64(len(store.jobs) + 1), Coordinates: coordinates, Route: route, State: "queued", MaxAttempts: 3})
	return true, nil
}

func (store *memoryStore) ClaimEnrichment(_ context.Context, owner string) (Job, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, job := range store.jobs {
		if job.State == "queued" {
			job.State, job.LeaseOwner, job.Fence, job.Attempt = "running", owner, job.Fence+1, job.Attempt+1
			return *job, nil
		}
	}
	return Job{}, ErrNoJob
}

func (store *memoryStore) RenewEnrichment(context.Context, int64, string, int64) error { return nil }

func (store *memoryStore) CompleteEnrichment(_ context.Context, id int64, owner string, fence int64, outcome Outcome, errorCode string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, job := range store.jobs {
		if job.ID == id {
			if job.LeaseOwner != owner || job.Fence != fence {
				return ErrFenced
			}
			job.State, job.ErrorCode = "succeeded", errorCode
			if outcome == OutcomeUnavailable {
				job.State = "queued"
			}
			return nil
		}
	}
	return ErrFenced
}

func (store *memoryStore) ReapExpiredEnrichment(context.Context, int) (int64, error) { return 0, nil }

func (store *memoryStore) ComponentsForCoordinates(_ context.Context, coordinates Coordinates, _ int) ([][2]int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.occurrences[coordinates], nil
}

func (store *memoryStore) UpsertAssessment(_ context.Context, assessment Assessment) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.assessments[assessment.ComponentID] = assessment
	return nil
}

func (store *memoryStore) SnapshotCoordinates(context.Context, int64) ([]Coordinates, error) {
	return store.coordinates, nil
}

func (store *memoryStore) ComponentDeclarations(_ context.Context, componentID int64) (*string, *string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.declared[componentID], nil, nil
}

func TestWorkerEnqueuesOnlyRoutedEcosystemsAndAssesses(t *testing.T) {
	r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"version":"1.3.0","license":"MIT"}`)
	})
	registry, err := NewRegistry([]Route{r.route(t, "npm", "/")})
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	npm := Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"}
	store.coordinates = []Coordinates{npm, {Ecosystem: "maven", Namespace: "org.example", Name: "core", Version: "2.1.0"}, {Ecosystem: "golang", Namespace: "golang.org/x", Name: "text", Version: "0.14.0"}, {Ecosystem: "npm", Name: "versionless"}}
	store.occurrences[npm] = [][2]int64{{7, 3}, {8, 4}}
	mit := "MIT"
	store.declared[7] = &mit
	isc := "ISC"
	store.declared[8] = &isc
	worker := &Worker{Store: store, Registry: registry, Owner: "w"}
	created, err := worker.EnqueueSnapshot(t.Context(), 3)
	if err != nil || created != 1 {
		t.Fatalf("created = %d err = %v (only the npm coordinate has a route and a version)", created, err)
	}
	if created, err := worker.EnqueueSnapshot(t.Context(), 3); err != nil || created != 0 {
		t.Fatalf("second enqueue created %d", created)
	}
	processed, err := worker.RunOnce(t.Context())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if r.calls.Load() != 1 || len(store.evidence) != 1 || store.evidence[0].NormalizedExpression != "MIT" || store.evidence[0].Route != "npm:test" {
		t.Fatalf("calls=%d evidence=%+v", r.calls.Load(), store.evidence)
	}
	if store.assessments[7].Status != AssessmentResolved || store.assessments[8].Status != AssessmentConflict || len(store.assessments) != 2 {
		t.Fatalf("assessments = %+v", store.assessments)
	}
	if processed, err := worker.RunOnce(t.Context()); err != nil || processed {
		t.Fatalf("queue drained: processed=%v err=%v", processed, err)
	}
	if store.jobs[0].State != "succeeded" {
		t.Fatalf("job = %+v", store.jobs[0])
	}
}

func TestWorkerWithoutRoutesProducesNoTraffic(t *testing.T) {
	r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) { t.Error("registry was called") })
	registry, err := NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	store.coordinates = []Coordinates{{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"}}
	worker := &Worker{Store: store, Registry: registry, Owner: "w"}
	if created, err := worker.EnqueueSnapshot(t.Context(), 1); err != nil || created != 0 || len(store.jobs) != 0 {
		t.Fatalf("created=%d jobs=%d err=%v", created, len(store.jobs), err)
	}
	if processed, err := worker.RunOnce(t.Context()); err != nil || processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if r.calls.Load() != 0 || len(registry.Ecosystems()) != 0 {
		t.Fatalf("calls=%d ecosystems=%v", r.calls.Load(), registry.Ecosystems())
	}
}

func TestWorkerOutageRetainsEarlierEvidence(t *testing.T) {
	var down bool
	r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
		if down {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(writer, `{"version":"1.0.0","license":"Apache-2.0"}`)
	})
	registry, _ := NewRegistry([]Route{r.route(t, "npm", "/")})
	store := newMemoryStore()
	coordinates := Coordinates{Ecosystem: "npm", Name: "pkg", Version: "1.0.0"}
	store.occurrences[coordinates] = [][2]int64{{1, 1}}
	worker := &Worker{Store: store, Registry: registry, Owner: "w", Now: func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }}
	if _, err := store.EnqueueEnrichment(t.Context(), coordinates, "npm:test"); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.assessments[1].Status != AssessmentResolved || store.assessments[1].NormalizedExpression != "Apache-2.0" {
		t.Fatalf("initial = %+v", store.assessments[1])
	}
	down = true
	store.jobs = nil
	if _, err := store.EnqueueEnrichment(t.Context(), coordinates, "npm:test"); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.evidence) != 2 || store.evidence[1].Outcome != OutcomeUnavailable || store.evidence[1].ExpiresAt == nil {
		t.Fatalf("outage evidence = %+v", store.evidence)
	}
	if store.assessments[1].Status != AssessmentResolved || store.assessments[1].NormalizedExpression != "Apache-2.0" {
		t.Fatalf("outage replaced earlier evidence: %+v", store.assessments[1])
	}
	if store.jobs[0].State != "queued" {
		t.Fatalf("unavailable outcome must retry: %+v", store.jobs[0])
	}
}

func TestNewRegistryRejectsDuplicatesAndUnknownEcosystems(t *testing.T) {
	r := newRegistry(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := NewRegistry([]Route{r.route(t, "npm", "/"), r.route(t, "npm", "/other/")}); err == nil {
		t.Fatal("duplicate route accepted")
	}
	if _, err := NewRegistry([]Route{r.route(t, "cargo", "/")}); err == nil {
		t.Fatal("unsupported ecosystem accepted")
	}
	registry, err := NewRegistry([]Route{r.route(t, "maven", "/"), r.route(t, "nuget", "/")})
	if err != nil || len(registry.Ecosystems()) != 2 || registry.Ecosystems()[0] != "nuget" {
		t.Fatalf("registry = %v %v", registry, err)
	}
}
