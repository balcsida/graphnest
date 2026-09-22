package license

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Registry maps ecosystems to configured resolvers. An ecosystem without a
// route has no resolver and its components stay at their producer-declared
// assessment; nothing is fetched for them.
type Registry struct {
	resolvers map[string]Resolver
	routes    map[string]string
}

func NewRegistry(routes []Route) (*Registry, error) {
	registry := &Registry{resolvers: map[string]Resolver{}, routes: map[string]string{}}
	for _, route := range routes {
		var resolver Resolver
		var err error
		switch route.Ecosystem {
		case "npm":
			resolver, err = NewNPMResolver(route)
		case "nuget":
			resolver, err = NewNuGetResolver(route)
		case "maven":
			resolver, err = NewMavenResolver(route)
		default:
			return nil, errors.New("unsupported registry ecosystem " + route.Ecosystem)
		}
		if err != nil {
			return nil, err
		}
		if _, duplicate := registry.resolvers[route.Ecosystem]; duplicate {
			return nil, errors.New("duplicate registry route for " + route.Ecosystem)
		}
		registry.resolvers[route.Ecosystem] = resolver
		registry.routes[route.Ecosystem] = route.Name
	}
	return registry, nil
}

// Route returns the configured route name for an ecosystem.
func (registry *Registry) Route(ecosystem string) (string, bool) {
	name, ok := registry.routes[ecosystem]
	return name, ok
}

func (registry *Registry) Resolver(ecosystem string) (Resolver, bool) {
	resolver, ok := registry.resolvers[ecosystem]
	return resolver, ok
}

// Ecosystems lists configured ecosystems in a stable order.
func (registry *Registry) Ecosystems() []string {
	var names []string
	for _, ecosystem := range []string{"npm", "nuget", "maven"} {
		if _, ok := registry.resolvers[ecosystem]; ok {
			names = append(names, ecosystem)
		}
	}
	return names
}

// Worker turns published snapshots into enrichment jobs and jobs into
// immutable evidence and rebuilt assessments.
type Worker struct {
	Store    Store
	Registry *Registry
	Owner    string
	Logger   *slog.Logger
	Observer interface {
		ObserveSupplyChainEnrichment(outcome string, duration time.Duration)
	}
	Poll time.Duration
	Now  func() time.Time
}

func (worker *Worker) now() time.Time {
	if worker.Now != nil {
		return worker.Now().UTC()
	}
	return time.Now().UTC()
}

func (worker *Worker) logger() *slog.Logger {
	if worker.Logger == nil {
		return slog.Default()
	}
	return worker.Logger
}

// EnqueueSnapshot queues lookups for every resolvable coordinate of a
// snapshot whose ecosystem has a route, and writes an initial assessment for
// every component from its producer declaration alone so the inventory shows
// declared/unknown immediately rather than waiting for registries.
func (worker *Worker) EnqueueSnapshot(ctx context.Context, snapshotID int64) (int, error) {
	if worker.Registry == nil {
		return 0, nil
	}
	coordinates, err := worker.Store.SnapshotCoordinates(ctx, snapshotID)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, item := range coordinates {
		route, ok := worker.Registry.Route(item.Ecosystem)
		if !ok || item.Version == "" || item.Name == "" {
			continue
		}
		ok, err := worker.Store.EnqueueEnrichment(ctx, item, route)
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}
	return created, nil
}

// Run processes enrichment jobs until the context ends.
func (worker *Worker) Run(ctx context.Context) error {
	poll := worker.Poll
	if poll <= 0 {
		poll = 10 * time.Second
	}
	for {
		if _, err := worker.Store.ReapExpiredEnrichment(ctx, 100); err != nil && ctx.Err() == nil {
			worker.logger().Error("enrichment reap failed", "error", err)
		}
		processed, err := worker.RunOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			worker.logger().Error("enrichment failed", "error", err)
		}
		if processed && err == nil {
			continue
		}
		timer := time.NewTimer(poll + time.Duration(rand.Int64N(int64(poll)/4+1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// RunOnce claims one job, resolves it, stores evidence, and rebuilds the
// assessments of every latest-stream occurrence with those coordinates.
func (worker *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, err := worker.Store.ClaimEnrichment(ctx, worker.Owner)
	if errors.Is(err, ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	started := worker.now()
	resolver, ok := worker.Registry.Resolver(job.Coordinates.Ecosystem)
	if !ok {
		return true, worker.Store.CompleteEnrichment(ctx, job.ID, job.LeaseOwner, job.Fence, OutcomeRejected, "no_route")
	}
	evidence, err := resolver.Resolve(ctx, job.Coordinates)
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, worker.Store.CompleteEnrichment(ctx, job.ID, job.LeaseOwner, job.Fence, OutcomeUnavailable, "resolver_error")
	}
	evidence.Route = job.Route
	id, _, err := worker.Store.InsertLicenseEvidence(ctx, evidence)
	if err != nil {
		return true, err
	}
	evidence.ID = id
	if err := worker.Store.CompleteEnrichment(ctx, job.ID, job.LeaseOwner, job.Fence, evidence.Outcome, ""); err != nil {
		if errors.Is(err, ErrFenced) {
			// The evidence row is still valid (it is what the registry said);
			// only the job bookkeeping belongs to another lease.
			return true, nil
		}
		return true, err
	}
	if worker.Observer != nil {
		worker.Observer.ObserveSupplyChainEnrichment(string(evidence.Outcome), worker.now().Sub(started))
	}
	return true, worker.Reassess(ctx, job.Coordinates)
}

// Reassess rebuilds assessments for every latest-stream occurrence of the
// coordinates from the newest evidence per source/route.
func (worker *Worker) Reassess(ctx context.Context, coordinates Coordinates) error {
	evidence, err := worker.Store.LatestLicenseEvidence(ctx, coordinates)
	if err != nil {
		return err
	}
	occurrences, err := worker.Store.ComponentsForCoordinates(ctx, coordinates, 10000)
	if err != nil {
		return err
	}
	for _, occurrence := range occurrences {
		declared, concluded, err := worker.declarations(ctx, occurrence[0])
		if err != nil {
			return err
		}
		if err := worker.Store.UpsertAssessment(ctx, Assess(occurrence[0], occurrence[1], declared, concluded, evidence, worker.now())); err != nil {
			return err
		}
	}
	return nil
}

// Declarations is optional: stores that can return the producer's raw values
// per component implement it; otherwise assessments use registry evidence only.
type Declarations interface {
	ComponentDeclarations(ctx context.Context, componentID int64) (declared, concluded *string, err error)
}

func (worker *Worker) declarations(ctx context.Context, componentID int64) (*string, *string, error) {
	if store, ok := worker.Store.(Declarations); ok {
		return store.ComponentDeclarations(ctx, componentID)
	}
	return nil, nil, nil
}
