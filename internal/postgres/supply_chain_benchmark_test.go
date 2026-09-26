//go:build integration

package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain"
)

// TestSupplyChainPortfolioQueryPlans seeds a representative synthetic
// inventory (200 repositories x 250 components, 50,000 occurrences over
// ~5,000 unique coordinates) and records EXPLAIN ANALYZE output for the
// portfolio queries when GRAPHNEST_TEST_SUPPLY_CHAIN_PLANS names an output
// file. Without the variable it only asserts the queries complete. Timings
// are environment-specific evidence, not a latency promise.
func TestSupplyChainPortfolioQueryPlans(t *testing.T) {
	if os.Getenv("GRAPHNEST_TEST_SUPPLY_CHAIN_PLANS") == "" {
		t.Skip("set GRAPHNEST_TEST_SUPPLY_CHAIN_PLANS to seed 50,000 occurrences and record plans")
	}
	store := migratedStore(t)
	const repositories, componentsPer, uniqueVersions = 200, 250, 5000
	if err := store.UpsertInstallation(t.Context(), InstallationUpdate{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, repositories)
	started := time.Now()
	for index := range repositories {
		repo, err := store.UpsertRepository(t.Context(), RepositoryUpdate{GitHubID: int64(1000 + index), InstallationID: 10, Owner: "acme", Name: fmt.Sprintf("repo-%03d", index), CloneURL: "c", WebURL: "w", DefaultBranch: "main", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, repo.ID)
		var packages []string
		var relationships []string
		for component := range componentsPer {
			coordinate := (index*7 + component*13) % uniqueVersions
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
	seedDuration := time.Since(started)
	if _, err := store.pool.Exec(t.Context(), `analyze supply_chain_components; analyze supply_chain_snapshots; analyze supply_chain_streams; analyze supply_chain_component_assessments`); err != nil {
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
		rows, err := store.pool.Query(t.Context(), "explain (analyze, buffers, format text) "+query, args...)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		fmt.Fprintf(&report, "## %s\n\n```\n", label)
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			report.WriteString(line + "\n")
		}
		rows.Close()
		fmt.Fprintf(&report, "```\n\nWall time including plan output: %s\n\n", time.Since(startedQuery).Round(time.Millisecond))
	}
	staleBefore := time.Now().Add(-48 * time.Hour)
	run("overview counts", `select count(*) filter (where st.latest_snapshot_id is not null), count(*) filter (where snap.collected_at < $3), coalesce(sum(snap.component_count), 0)
		from unnest($1::bigint[]) as r(id) left join supply_chain_streams st on st.repository_id=r.id and st.stream_key=$2 left join supply_chain_snapshots snap on snap.id=st.latest_snapshot_id`, ids, supplychain.StreamGitHubSource, staleBefore)
	run("unique coordinates", `select count(distinct (c.ecosystem, coalesce(c.purl_namespace, ''), coalesce(c.purl_name, c.name), coalesce(c.purl_version, c.version, ''))) from supply_chain_streams st
		join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id where st.repository_id=any($1) and st.stream_key=$2`, ids, supplychain.StreamGitHubSource)
	run("portfolio page (first 100)", `with occurrences as (
			select c.*, st.repository_id, snap.collected_at, r.github_id, a.status as assessment_status, a.normalized_expression,
				coalesce(c.ecosystem, '') as eco, coalesce(c.purl_namespace, '') as ns, coalesce(c.purl_name, c.name) as pname, coalesce(c.purl_version, c.version, '') as pversion
			from supply_chain_streams st join supply_chain_snapshots snap on snap.id=st.latest_snapshot_id join repositories r on r.id=st.repository_id
			join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id left join supply_chain_component_assessments a on a.component_id=c.id
			where st.repository_id=any($1) and st.stream_key=$2
		), grouped as (
			select eco || chr(1) || ns || chr(1) || pname || chr(1) || pversion as key, count(distinct repository_id) as repositories, count(*) as occurrences from occurrences group by eco, ns, pname, pversion
		) select key, repositories, occurrences from grouped where key>'' order by key limit 101`, ids, supplychain.StreamGitHubSource)
	run("coordinate occurrences", `select r.github_id, c.element_id from supply_chain_streams st join repositories r on r.id=st.repository_id join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
		where st.repository_id=any($1) and st.stream_key=$2 and coalesce(c.ecosystem, '')='npm' and coalesce(c.purl_namespace, '')='' and coalesce(c.purl_name, c.name)='pkg-0005' and coalesce(c.purl_version, c.version, '')='5.0.2' limit 101`, ids, supplychain.StreamGitHubSource)
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
