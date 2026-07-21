//go:build integration

package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/scipgraph"
	"github.com/jackc/pgx/v5"
)

const (
	globalSymbol         = "scip go example.com/grepnest v1 pkg/Item#"
	implementationSymbol = "scip go example.com/grepnest v1 pkg/Concrete#"
	localSymbol          = "local 0"
	definitionRole       = int32(1)
)

func TestReplaceSCIPIsAtomicAndCurrent(t *testing.T) {
	store := migratedStore(t)
	repositoryID := seedReadyRepository(t, store, 101, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("a.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("b.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OccurrenceAt(t.Context(), repositoryID, testSHA('a'), "a.go", 0, 1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("removed occurrence err = %v", err)
	}
	if _, err := store.OccurrenceAt(t.Context(), repositoryID, testSHA('b'), "b.go", 0, 1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale occurrence err = %v", err)
	}

	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('b'), uploadWith("stale.go", globalSymbol, definitionRole)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale replacement err = %v", err)
	}
	if occurrence, err := store.OccurrenceAt(t.Context(), repositoryID, testSHA('a'), "b.go", 0, 1); err != nil || occurrence.Path != "b.go" {
		t.Fatalf("current occurrence = %#v, err = %v", occurrence, err)
	}
	duplicate := uploadWith("duplicate.go", globalSymbol, definitionRole)
	duplicate.Occurrences = append(duplicate.Occurrences, duplicate.Occurrences[0])
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), duplicate); err == nil {
		t.Fatal("duplicate replacement succeeded")
	}
	if occurrence, err := store.OccurrenceAt(t.Context(), repositoryID, testSHA('a'), "b.go", 0, 1); err != nil || occurrence.Path != "b.go" {
		t.Fatalf("occurrence after rollback = %#v, err = %v", occurrence, err)
	}
}

func TestSCIPTablesCascadeWithRepository(t *testing.T) {
	store := migratedStore(t)
	repositoryID := seedReadyRepository(t, store, 101, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), scipgraph.Upload{
		Occurrences:   []scipgraph.Occurrence{{Path: "a.go", Symbol: globalSymbol, EndCharacter: 2}},
		Relationships: []scipgraph.Relationship{{Source: globalSymbol, Target: implementationSymbol, Implementation: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into repository_packages
		(repository_id, source, relation, purl, manager, name, version)
		values ($1, 'manual', 'provides', 'pkg:golang/example.com/grepnest@v1', 'gomod', 'example.com/grepnest', 'v1')`, repositoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), "delete from repositories where id=$1", repositoryID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"scip_uploads", "scip_occurrences", "scip_relationships", "repository_packages"} {
		var count int
		if err := store.pool.QueryRow(t.Context(), "select count(*) from "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows = %d, err = %v", table, count, err)
		}
	}
}

func TestSCIPLocationsAuthorizeAndScopeSymbols(t *testing.T) {
	store := migratedStore(t)
	firstID := seedReadyRepository(t, store, 101, testSHA('a'))
	secondID := seedReadyRepository(t, store, 102, testSHA('b'))
	thirdID := seedReadyRepository(t, store, 103, testSHA('c'))
	fourthID := seedReadyRepository(t, store, 104, testSHA('d'))

	if err := store.ReplaceSCIP(t.Context(), firstID, testSHA('a'), scipgraph.Upload{Occurrences: []scipgraph.Occurrence{
		{Path: "origin.go", Symbol: globalSymbol, EndCharacter: 2},
		{Path: "local.go", Symbol: localSymbol, EndCharacter: 2, Local: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSCIP(t.Context(), secondID, testSHA('b'), scipgraph.Upload{Occurrences: []scipgraph.Occurrence{
		{Path: "definition.go", Symbol: globalSymbol, EndCharacter: 2, Roles: definitionRole},
		{Path: "local.go", Symbol: localSymbol, EndCharacter: 2, Roles: definitionRole, Local: true},
		{Path: "implementation.go", Symbol: implementationSymbol, EndCharacter: 2, Roles: definitionRole},
	}, Relationships: []scipgraph.Relationship{{Source: implementationSymbol, Target: globalSymbol, Implementation: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSCIP(t.Context(), thirdID, testSHA('c'), uploadWith("forbidden.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSCIP(t.Context(), fourthID, testSHA('d'), uploadWith("forbidden-origin.go", globalSymbol, 0)); err != nil {
		t.Fatal(err)
	}

	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101, 102}}
	origin, err := store.OccurrenceAt(t.Context(), firstID, testSHA('a'), "origin.go", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	definitions, truncated, err := store.Locations(t.Context(), principal, origin, "definitions", 10)
	if err != nil || truncated || len(definitions) != 1 || definitions[0].RepositoryID != 102 || definitions[0].Path != "definition.go" {
		t.Fatalf("definitions = %#v, truncated = %v, err = %v", definitions, truncated, err)
	}
	definition, err := store.OccurrenceAt(t.Context(), secondID, testSHA('b'), "definition.go", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	references, truncated, err := store.Locations(t.Context(), principal, definition, "references", 10)
	if err != nil || truncated || len(references) != 1 || references[0].RepositoryID != 101 || references[0].Path != "origin.go" {
		t.Fatalf("references = %#v, truncated = %v, err = %v", references, truncated, err)
	}
	implementations, truncated, err := store.Locations(t.Context(), principal, origin, "implementations", 10)
	if err != nil || truncated || len(implementations) != 1 || implementations[0].RepositoryID != 102 || implementations[0].Path != "implementation.go" {
		t.Fatalf("implementations = %#v, truncated = %v, err = %v", implementations, truncated, err)
	}

	localOrigin, err := store.OccurrenceAt(t.Context(), firstID, testSHA('a'), "local.go", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	locations, truncated, err := store.Locations(t.Context(), principal, localOrigin, "definitions", 10)
	if err != nil || truncated || len(locations) != 0 {
		t.Fatalf("local definitions = %#v, truncated = %v, err = %v", locations, truncated, err)
	}

	unauthorizedOrigin, err := store.OccurrenceAt(t.Context(), fourthID, testSHA('d'), "forbidden-origin.go", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	locations, truncated, err = store.Locations(t.Context(), principal, unauthorizedOrigin, "definitions", 10)
	if err != nil || truncated || len(locations) != 0 {
		t.Fatalf("unauthorized-origin definitions = %#v, truncated = %v, err = %v", locations, truncated, err)
	}
}

func TestSCIPLocationsUseDeterministicTotalOrder(t *testing.T) {
	store := migratedStore(t)
	repositoryID := seedReadyRepository(t, store, 101, testSHA('a'))
	const (
		firstImplementation  = "scip go example.com/grepnest v1 pkg/A#"
		secondImplementation = "scip go example.com/grepnest v1 pkg/B#"
		lastImplementation   = "scip go example.com/grepnest v1 pkg/Z#"
	)
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), scipgraph.Upload{
		Occurrences: []scipgraph.Occurrence{
			{Path: "origin.go", Symbol: globalSymbol, EndCharacter: 2},
			{Path: "implementations.go", Symbol: lastImplementation, EndLine: 1, Roles: definitionRole},
			{Path: "implementations.go", Symbol: secondImplementation, EndCharacter: 5, Roles: definitionRole},
			{Path: "implementations.go", Symbol: firstImplementation, EndCharacter: 5, Roles: definitionRole},
		},
		Relationships: []scipgraph.Relationship{
			{Source: lastImplementation, Target: globalSymbol, Implementation: true},
			{Source: secondImplementation, Target: globalSymbol, Implementation: true},
			{Source: firstImplementation, Target: globalSymbol, Implementation: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	origin, err := store.OccurrenceAt(t.Context(), repositoryID, testSHA('a'), "origin.go", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	locations, truncated, err := store.Locations(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, origin, "implementations", 10)
	if err != nil || truncated || len(locations) != 3 {
		t.Fatalf("locations = %#v, truncated = %v, err = %v", locations, truncated, err)
	}
	for index, symbol := range []string{firstImplementation, secondImplementation, lastImplementation} {
		if locations[index].Symbol != symbol {
			t.Fatalf("locations[%d].Symbol = %q, want %q", index, locations[index].Symbol, symbol)
		}
	}
}

func TestSCIPLocationsReportTruncation(t *testing.T) {
	store := migratedStore(t)
	repositoryID := seedReadyRepository(t, store, 101, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), scipgraph.Upload{Occurrences: []scipgraph.Occurrence{
		{Path: "origin.go", Symbol: globalSymbol, EndCharacter: 2},
		{Path: "one.go", Symbol: globalSymbol, EndCharacter: 2, Roles: definitionRole},
		{Path: "two.go", Symbol: globalSymbol, EndCharacter: 2, Roles: definitionRole},
	}}); err != nil {
		t.Fatal(err)
	}
	origin, err := store.OccurrenceAt(t.Context(), repositoryID, testSHA('a'), "origin.go", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	locations, truncated, err := store.Locations(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, origin, "definitions", 1)
	if err != nil || !truncated || len(locations) != 1 {
		t.Fatalf("locations = %#v, truncated = %v, err = %v", locations, truncated, err)
	}
}

func uploadWith(path, symbol string, roles int32) scipgraph.Upload {
	return scipgraph.Upload{ProjectRoot: "file:///src", IndexerName: "test", IndexerVersion: "1", Occurrences: []scipgraph.Occurrence{{
		Path: path, Symbol: symbol, EndCharacter: 2, Roles: roles,
	}}}
}

func seedReadyRepository(t *testing.T, store *Store, githubID int64, sha string) int64 {
	t.Helper()
	if err := store.UpsertInstallation(t.Context(), InstallationUpdate{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.UpsertRepository(t.Context(), RepositoryUpdate{
		GitHubID: githubID, InstallationID: 10, Owner: "acme", Name: fmt.Sprintf("repo-%d", githubID), CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), "update repositories set indexed_sha=$2, desired_sha=$2, status='ready' where id=$1", repository.ID, sha); err != nil {
		t.Fatal(err)
	}
	return repository.ID
}
