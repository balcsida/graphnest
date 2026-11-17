//go:build integration

package postgres

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain"
)

// seedSyntheticInventory publishes one SPDX snapshot of componentsPer
// packages for each of the repositories, drawn from uniqueVersions
// coordinates so that repositories share packages, and returns the repository
// IDs. Another generation publishes different documents for the same
// repositories, which leaves the earlier snapshots behind as history.
func seedSyntheticInventory(t *testing.T, store *Store, repositories, componentsPer, uniqueVersions, generation int) []int64 {
	t.Helper()
	if err := store.UpsertInstallation(t.Context(), InstallationUpdate{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, repositories)
	for index := range repositories {
		repo, err := store.UpsertRepository(t.Context(), RepositoryUpdate{GitHubID: int64(1000 + index), InstallationID: 10, Owner: "acme", Name: fmt.Sprintf("repo-%03d", index), CloneURL: "c", WebURL: "w", DefaultBranch: "main", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, repo.ID)
		var packages []string
		var relationships []string
		for component := range componentsPer {
			coordinate := (index*7 + component*13 + generation) % uniqueVersions
			ecosystem := []string{"npm", "maven", "nuget", "golang", "pypi"}[coordinate%5]
			name := fmt.Sprintf("pkg-%04d", coordinate)
			version := fmt.Sprintf("%d.%d.%d", coordinate%9, coordinate%5, coordinate%3)
			id := fmt.Sprintf("SPDXRef-%d", component)
			packages = append(packages, fmt.Sprintf(`{"SPDXID":%q,"name":%q,"versionInfo":%q,"licenseDeclared":"NOASSERTION","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:%s/%s@%s"}]}`, id, ecosystem+":"+name, version, ecosystem, name, version))
			relationships = append(relationships, fmt.Sprintf(`{"spdxElementId":"SPDXRef-root","relationshipType":"DEPENDS_ON","relatedSpdxElement":%q}`, id))
		}
		document := []byte(fmt.Sprintf(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"acme/repo-%03d","creationInfo":{"created":"2026-09-01T00:00:00Z","creators":["Tool: GitHub.com-Dependency-Graph"]},"documentDescribes":["SPDXRef-root"],"packages":[{"SPDXID":"SPDXRef-root","name":"root"},%s],"relationships":[%s]}`,
			index, strings.Join(packages, ","), strings.Join(relationships, ",")))
		normalized, err := supplychain.NormalizeSPDX23(document, supplychain.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PublishSupplyChainSnapshot(t.Context(), SupplyChainPublication{RepositoryID: repo.ID, Producer: supplychain.ProducerGitHub, Subject: supplychain.SubjectSource, StreamKey: supplychain.StreamGitHubSource,
			Format: supplychain.FormatSPDX23JSON, MediaType: "application/json", Document: document, Normalized: normalized, CollectedAt: time.Now(), StartedAt: time.Now(), SubjectAssurance: supplychain.AssuranceUnknown}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(t.Context(), `analyze supply_chain_components; analyze supply_chain_snapshots; analyze supply_chain_streams; analyze supply_chain_component_assessments`); err != nil {
		t.Fatal(err)
	}
	return ids
}

// componentRowsRead adds up the supply_chain_components rows the scans of an
// EXPLAIN (ANALYZE, FORMAT JSON) plan produced or filtered out.
func componentRowsRead(node map[string]any) float64 {
	read := 0.0
	if node["Relation Name"] == "supply_chain_components" {
		rows, _ := node["Actual Rows"].(float64)
		removed, _ := node["Rows Removed by Filter"].(float64)
		loops, _ := node["Actual Loops"].(float64)
		read = (rows + removed) * loops
	}
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		read += componentRowsRead(child.(map[string]any))
	}
	return read
}

// TestSupplyChainPortfolioPageReadsOnlyItsPage guards the component listing
// against aggregating the whole scope for every page, which made a page cost
// as much as the inventory is large: the first page and a page from the
// middle have to read a small fraction of the scope's 10,040 occurrences, and
// none of the 10,040 an earlier generation of snapshots left behind.
func TestSupplyChainPortfolioPageReadsOnlyItsPage(t *testing.T) {
	store := migratedStore(t)
	// Every snapshot holds its packages and the root package that depends on them.
	const repositories, componentsPer, uniqueVersions, occurrences = 40, 250, 2000, 40 * (250 + 1)
	seedSyntheticInventory(t, store, repositories, componentsPer, uniqueVersions, 0)
	ids := seedSyntheticInventory(t, store, repositories, componentsPer, uniqueVersions, 1)
	scope, err := store.supplyChainScope(t.Context(), ids, supplychain.StreamGitHubSource)
	if err != nil || len(scope.snapshots) != repositories {
		t.Fatalf("scope = %d snapshots, %v", len(scope.snapshots), err)
	}

	// Every coordinate is listed once, in the bytewise order the cursor continues from.
	filter := supplychain.PortfolioFilter{RepositoryIDs: ids, StreamKey: supplychain.StreamGitHubSource, Limit: 101}
	var keys []string
	listed := 0
	for {
		page, err := store.SupplyChainPortfolioComponents(t.Context(), filter)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page {
			if len(keys) > 0 && item.Key <= keys[len(keys)-1] {
				t.Fatalf("key %q follows %q", item.Key, keys[len(keys)-1])
			}
			keys = append(keys, item.Key)
			listed += item.OccurrenceCount
		}
		if len(page) < filter.Limit {
			break
		}
		filter.AfterKey = keys[len(keys)-1]
	}
	var unique int
	if err := store.pool.QueryRow(t.Context(), `select count(distinct `+coordinateKey+`) from supply_chain_components c where c.snapshot_id=any($1)`, scope.snapshots).Scan(&unique); err != nil {
		t.Fatal(err)
	}
	if len(keys) != unique || listed != occurrences {
		t.Fatalf("listed %d coordinates with %d occurrences, want %d with %d", len(keys), listed, unique, occurrences)
	}

	for _, after := range []string{"", keys[len(keys)/2]} {
		filter.AfterKey, filter.Limit = after, 11
		query, arguments := portfolioComponentsQuery(filter)
		var output []byte
		if err := store.pool.QueryRow(t.Context(), "explain (analyze, format json) "+query, append([]any{customPlan, scope.snapshots}, arguments...)...).Scan(&output); err != nil {
			t.Fatal(err)
		}
		var plans []map[string]any
		if err := json.Unmarshal(output, &plans); err != nil {
			t.Fatal(err)
		}
		if read := componentRowsRead(plans[0]["Plan"].(map[string]any)); read > occurrences/10 {
			t.Fatalf("a page of %d coordinates after %q read %.0f component rows of %d in scope:\n%s", filter.Limit, after, read, occurrences, output)
		}
	}
}

// TestSupplyChainPortfolioQueryPlans seeds a representative synthetic
// inventory (200 repositories x 250 components, 50,000 occurrences over
// ~5,000 unique coordinates) and records EXPLAIN ANALYZE output for the
// portfolio statements the store runs when GRAPHNEST_TEST_SUPPLY_CHAIN_PLANS
// names an output file. Without the variable it only asserts the queries complete. Timings
// are environment-specific evidence, not a latency promise.
func TestSupplyChainPortfolioQueryPlans(t *testing.T) {
	if os.Getenv("GRAPHNEST_TEST_SUPPLY_CHAIN_PLANS") == "" {
		t.Skip("set GRAPHNEST_TEST_SUPPLY_CHAIN_PLANS to seed 50,000 occurrences and record plans")
	}
	store := migratedStore(t)
	const repositories, componentsPer, uniqueVersions = 200, 250, 5000
	started := time.Now()
	ids := seedSyntheticInventory(t, store, repositories, componentsPer, uniqueVersions, 0)
	seedDuration := time.Since(started)
	scope, err := store.supplyChainScope(t.Context(), ids, supplychain.StreamGitHubSource)
	if err != nil {
		t.Fatal(err)
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# Supply-chain portfolio query plans\n\nDataset: %d repositories x %d components = %d occurrences, ~%d unique coordinates; seeded in %s.\nPostgreSQL: ", repositories, componentsPer, repositories*componentsPer, uniqueVersions, seedDuration.Round(time.Millisecond))
	var version string
	if err := store.pool.QueryRow(t.Context(), `select version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	report.WriteString(version + "\n\n")
	run := func(label string, query string, args ...any) {
		startedQuery := time.Now()
		rows, err := store.pool.Query(t.Context(), "explain (analyze, buffers, format text) "+query, append([]any{customPlan}, args...)...)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		fmt.Fprintf(&report, "## %s\n\n```\n", label)
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			// The key separator is chr(1); keep the recorded plan printable.
			report.WriteString(strings.ReplaceAll(line, "\x01", `\x01`) + "\n")
		}
		rows.Close()
		fmt.Fprintf(&report, "```\n\nWall time including plan output: %s\n\n", time.Since(startedQuery).Round(time.Millisecond))
	}
	staleBefore := time.Now().Add(-48 * time.Hour)
	run("overview counts", `select count(*) filter (where st.latest_snapshot_id is not null), count(*) filter (where snap.collected_at < $3), coalesce(sum(snap.component_count), 0)
		from unnest($1::bigint[]) as r(id) left join supply_chain_streams st on st.repository_id=r.id and st.stream_key=$2 left join supply_chain_snapshots snap on snap.id=st.latest_snapshot_id`, ids, supplychain.StreamGitHubSource, staleBefore)
	run("coordinates per ecosystem (overview)", portfolioCoordinatesQuery, scope.snapshots)
	run("assessment counts (overview and facets)", `select status, count(*) from supply_chain_component_assessments where snapshot_id=any($1) group by 1`, scope.snapshots)
	page := supplychain.PortfolioFilter{Limit: 101}
	query, arguments := portfolioComponentsQuery(page)
	run("portfolio page (first 100)", query, append([]any{scope.snapshots}, arguments...)...)
	page.AfterKey = "npm\x01\x01pkg-2500\x017.0.1"
	query, arguments = portfolioComponentsQuery(page)
	run("portfolio page (100 from the middle)", query, append([]any{scope.snapshots}, arguments...)...)
	page = supplychain.PortfolioFilter{Limit: 101, Search: "pkg-12"}
	query, arguments = portfolioComponentsQuery(page)
	run("portfolio page (first 100 matching a search)", query, append([]any{scope.snapshots}, arguments...)...)
	run("coordinate occurrences", `select r.github_id, c.element_id from supply_chain_streams st join repositories r on r.id=st.repository_id join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
		where st.repository_id=any($1) and st.stream_key=$2 and coalesce(c.ecosystem, '')='npm' and coalesce(c.purl_namespace, '')='' and coalesce(c.purl_name, c.name)='pkg-0005' and coalesce(c.purl_version, c.version, '')='5.0.2'
		and `+coordinateKey+`='npm' || chr(1) || '' || chr(1) || 'pkg-0005' || chr(1) || '5.0.2' limit 101`, ids, supplychain.StreamGitHubSource)
	run("review queue (no results yet)", `select c.id from supply_chain_streams st join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
		left join supply_chain_component_assessments a on a.component_id=c.id left join supply_chain_policy_results pr on pr.component_id=c.id and pr.current
		where st.repository_id=any($1) and st.stream_key=$2 and c.id>0 and coalesce(pr.verdict, 'unknown') <> 'approved' order by c.id limit 101`, ids, supplychain.StreamGitHubSource)
	if path := os.Getenv("GRAPHNEST_TEST_SUPPLY_CHAIN_PLANS"); path != "" {
		if err := os.WriteFile(path, []byte(report.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("seeded %d occurrences in %s", repositories*componentsPer, seedDuration.Round(time.Millisecond))
}
