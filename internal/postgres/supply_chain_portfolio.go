package postgres

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/jackc/pgx/v5"
)

// Portfolio queries aggregate over the latest snapshot of one stream per
// authorized repository. Every query takes the caller's authorized repository
// IDs and never reads beyond them; totals, facets, and pages are computed
// inside that scope.

// coordinateKey is the key the portfolio groups and pages by. It must stay
// identical to the expression that migration 042 indexes, or the planner
// stops using supply_chain_components_coordinate_key.
const coordinateKey = `((coalesce(c.ecosystem, '') || chr(1) || coalesce(c.purl_namespace, '') || chr(1) || coalesce(c.purl_name, c.name) || chr(1) || coalesce(c.purl_version, c.version, '')) collate "C")`

// packageKey is the coordinate key without the version; every coordinate key
// of a package starts with it and chr(1).
const packageKey = `((coalesce(c.ecosystem, '') || chr(1) || coalesce(c.purl_namespace, '') || chr(1) || coalesce(c.purl_name, c.name)) collate "C")`

// ecosystemFilter keeps the coordinates of the ecosystem ($2). The ecosystem
// leads the coordinate key, so the filter is a key range an index scan can
// start and stop at.
const ecosystemFilter = `($2='' or (` + coordinateKey + ` >= $2 || chr(1) and ` + coordinateKey + ` < $2 || chr(2)))`

// occurrenceFilters keeps the occurrences of c (and its assessment a) that
// match the search ($3), license expression ($4) and assessment status ($5)
// within the snapshots $1. Naming the assessment's own snapshot is redundant
// with the scope of c, and is what lets a license or status filter start
// from the assessments of the scope instead of reading every occurrence.
const occurrenceFilters = `($3='' or lower(coalesce(c.purl_name, c.name)) like '%'||lower($3)||'%' or lower(coalesce(c.purl, '')) like '%'||lower($3)||'%' or lower(c.name) like '%'||lower($3)||'%')
	and ($4='' or (lower(a.normalized_expression)=lower($4) and a.snapshot_id=any($1)))
	and ($5='' or ($5='unassessed' and a.component_id is null) or (a.status=$5 and a.snapshot_id=any($1)))`

// portfolioCoordinatesQuery reads the occurrences of the snapshots $1 once:
// a row per coordinate, folded into occurrence, coordinate, PURL-less and
// version-less counts per ecosystem.
const portfolioCoordinatesQuery = `select ecosystem, sum(occurrences), count(*), sum(without_purl), sum(without_version) from (
		select min(coalesce(c.ecosystem, '')) as ecosystem, count(*) as occurrences, count(*) filter (where c.purl is null) as without_purl,
			count(*) filter (where coalesce(c.purl_version, c.version, '')='') as without_version
		from supply_chain_components c where c.snapshot_id=any($1) group by ` + coordinateKey + `
	) coordinates group by ecosystem`

// portfolioGraphQuery ranks the packages of the snapshots $1 by the
// repositories using them in one pass, keeps those shared by at least $6
// repositories and returns the $7 most shared. Assessments are read for the
// occurrences of the returned packages only. A repository has one latest
// snapshot per stream, so distinct snapshots count repositories.
const portfolioGraphQuery = `with packages as (
		select ` + packageKey + ` as package, count(distinct c.snapshot_id) as repositories, array_agg(distinct c.snapshot_id) as snapshots,
			array_remove(array_agg(distinct coalesce(c.purl_version, c.version, '')), '') as versions, array_agg(c.id) as occurrences
		from supply_chain_components c
		left join supply_chain_component_assessments a on a.component_id=c.id
		where c.snapshot_id=any($1) and ` + ecosystemFilter + ` and ` + occurrenceFilters + `
		group by 1 having count(distinct c.snapshot_id) >= $6
	), ranked as (
		select *, count(*) over () as matching from packages order by repositories desc, package limit $7
	)
	select named.ecosystem, named.namespace, named.name, ranked.repositories, ranked.matching, ranked.versions, ranked.snapshots, assessed.statuses, assessed.expressions
	from ranked
	cross join lateral (
		select coalesce(c.ecosystem, '') as ecosystem, coalesce(c.purl_namespace, '') as namespace, coalesce(c.purl_name, c.name) as name
		from supply_chain_components c where c.id=ranked.occurrences[1]
	) named
	cross join lateral (
		select array_agg(distinct coalesce(a.status, 'unassessed')) as statuses,
			coalesce(array_agg(distinct a.normalized_expression) filter (where a.normalized_expression<>''), '{}') as expressions
		from unnest(ranked.occurrences) as occurrence(id)
		left join supply_chain_component_assessments a on a.component_id=occurrence.id
	) assessed
	order by ranked.repositories desc, ranked.package`

// customPlan has PostgreSQL plan a statement for the arguments of each call.
// The scope ranges from one snapshot to all of them and the filters from none
// to very selective, while a cached generic plan assumes ten snapshots and
// keeps filters it cannot see through.
const customPlan = pgx.QueryExecModeDescribeExec

// portfolioScope is what a portfolio query reads: the stream's latest
// snapshot of each authorized repository. Queries take the snapshot IDs, so
// the scope is checked inside an index without visiting the components of
// retained history, and a result names repositories by their snapshot.
type portfolioScope struct {
	snapshots  []int64
	bySnapshot map[int64]scopeSnapshot
}

type scopeSnapshot struct {
	githubID    int64
	collectedAt time.Time
}

func (s *Store) supplyChainScope(ctx context.Context, repositoryIDs []int64, streamKey string) (portfolioScope, error) {
	scope := portfolioScope{snapshots: []int64{}, bySnapshot: map[int64]scopeSnapshot{}}
	rows, err := s.pool.Query(ctx, `select snap.id, r.github_id, snap.collected_at from supply_chain_streams st
		join supply_chain_snapshots snap on snap.id=st.latest_snapshot_id
		join repositories r on r.id=st.repository_id
		where st.repository_id=any($1) and st.stream_key=$2 order by snap.id`, repositoryIDs, streamKey)
	if err != nil {
		return scope, err
	}
	var id int64
	var snapshot scopeSnapshot
	_, err = pgx.ForEachRow(rows, []any{&id, &snapshot.githubID, &snapshot.collectedAt}, func() error {
		scope.snapshots = append(scope.snapshots, id)
		scope.bySnapshot[id] = snapshot
		return nil
	})
	return scope, err
}

// repositories returns the GitHub IDs of the snapshots' repositories in order.
func (scope portfolioScope) repositories(snapshots []int64) []int64 {
	ids := make([]int64, 0, len(snapshots))
	for _, snapshot := range snapshots {
		ids = append(ids, scope.bySnapshot[snapshot].githubID)
	}
	slices.Sort(ids)
	return ids
}

// topFacets orders facets by count, then value, and keeps the first limit.
func topFacets(facets []supplychain.FacetCount, limit int) []supplychain.FacetCount {
	sort.Slice(facets, func(i, j int) bool {
		if facets[i].Count != facets[j].Count {
			return facets[i].Count > facets[j].Count
		}
		return facets[i].Value < facets[j].Value
	})
	if len(facets) > limit {
		facets = facets[:limit]
	}
	return facets
}

// collectFacets reads (value, count) rows.
func collectFacets(rows pgx.Rows, err error) ([]supplychain.FacetCount, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (supplychain.FacetCount, error) {
		var facet supplychain.FacetCount
		return facet, row.Scan(&facet.Value, &facet.Count)
	})
}

// supplyChainAssessmentCounts counts the scope's assessed occurrences by
// status from the (snapshot_id, status) index alone. Occurrences without an
// assessment row are the scope's occurrence total minus these.
func (s *Store) supplyChainAssessmentCounts(ctx context.Context, scope portfolioScope) ([]supplychain.FacetCount, error) {
	rows, err := s.pool.Query(ctx, `select status, count(*) from supply_chain_component_assessments where snapshot_id=any($1) group by 1`, customPlan, scope.snapshots)
	return collectFacets(rows, err)
}

// SupplyChainOverview computes the overview for the stream across the
// authorized repositories. staleBefore is the freshness cutoff.
func (s *Store) SupplyChainOverview(ctx context.Context, repositoryIDs []int64, streamKey string, staleBefore time.Time) (supplychain.PortfolioOverview, error) {
	overview := supplychain.PortfolioOverview{Repositories: len(repositoryIDs), AssessmentCounts: map[string]int{}, Ecosystems: []supplychain.FacetCount{}}
	if len(repositoryIDs) == 0 {
		return overview, nil
	}
	if err := s.pool.QueryRow(ctx, `select
			count(*) filter (where st.latest_snapshot_id is not null),
			count(*) filter (where st.latest_snapshot_id is not null and snap.collected_at < $3),
			count(*) filter (where st.last_outcome not in ('', 'published', 'unchanged')),
			count(*) filter (where st.latest_snapshot_id is null),
			count(*) filter (where st.opt_out),
			coalesce(sum(snap.component_count), 0), coalesce(sum(snap.warning_count), 0), min(snap.collected_at), max(snap.collected_at)
		from unnest($1::bigint[]) as r(id)
		left join supply_chain_streams st on st.repository_id=r.id and st.stream_key=$2
		left join supply_chain_snapshots snap on snap.id=st.latest_snapshot_id`, repositoryIDs, streamKey, staleBefore).
		Scan(&overview.WithInventory, &overview.Stale, &overview.Failed, &overview.NeverCollected, &overview.OptedOut, &overview.Occurrences, &overview.WarningTotal, &overview.OldestCollectedAt, &overview.NewestCollectedAt); err != nil {
		return supplychain.PortfolioOverview{}, err
	}
	// Repositories with no stream row at all are "never collected" too.
	overview.NeverCollected = overview.Repositories - overview.WithInventory
	scope, err := s.supplyChainScope(ctx, repositoryIDs, streamKey)
	if err != nil {
		return supplychain.PortfolioOverview{}, err
	}
	rows, err := s.pool.Query(ctx, portfolioCoordinatesQuery, customPlan, scope.snapshots)
	if err != nil {
		return supplychain.PortfolioOverview{}, err
	}
	occurrences := 0
	var ecosystem supplychain.FacetCount
	var coordinates, withoutPURL, withoutVersion int
	if _, err := pgx.ForEachRow(rows, []any{&ecosystem.Value, &ecosystem.Count, &coordinates, &withoutPURL, &withoutVersion}, func() error {
		occurrences += ecosystem.Count
		overview.UniqueCoordinates += coordinates
		overview.WithoutPURL += withoutPURL
		overview.WithoutVersion += withoutVersion
		overview.Ecosystems = append(overview.Ecosystems, ecosystem)
		return nil
	}); err != nil {
		return supplychain.PortfolioOverview{}, err
	}
	overview.Ecosystems = topFacets(overview.Ecosystems, 50)
	assessments, err := s.supplyChainAssessmentCounts(ctx, scope)
	if err != nil {
		return supplychain.PortfolioOverview{}, err
	}
	overview.UnassessedComponent = occurrences
	for _, assessment := range assessments {
		overview.AssessmentCounts[assessment.Value] = assessment.Count
		overview.UnassessedComponent -= assessment.Count
	}
	return overview, nil
}

// portfolioComponentsQuery returns the component page statement for the
// filter and the arguments that follow the scope's snapshots. It picks the
// page's keys first and aggregates only their occurrences, so a page costs
// what it returns rather than what the scope holds.
func portfolioComponentsQuery(filter supplychain.PortfolioFilter) (string, []any) {
	keys := `select ` + coordinateKey + ` as key from supply_chain_components c
		left join supply_chain_component_assessments a on a.component_id=c.id
		where c.snapshot_id=any($1) and ` + coordinateKey + ` > $6 and ` + ecosystemFilter + ` and ` + occurrenceFilters
	if filter.Search != "" || filter.LicenseExpression != "" || filter.AssessmentStatus != "" {
		// Reading the index in key order until the page is full is the plan to
		// want without these filters and the one to avoid with them: matches
		// may be anywhere, so it could read the whole index row by row. OFFSET 0
		// has the planner find the matches first and sort only those.
		keys = `select key from (` + keys + ` offset 0) matches`
	}
	// The scope is an index condition for the keys only: on the join below it
	// would be costed as one index descent per snapshot and key.
	return `with page as (` + keys + ` group by 1 order by 1 limit $7)
		select page.key, min(coalesce(c.ecosystem, '')), min(coalesce(c.purl_namespace, '')), min(coalesce(c.purl_name, c.name)), min(coalesce(c.purl_version, c.version, '')),
			max(coalesce(c.purl, '')), count(*), array_remove(array_agg(distinct a.status), null), max(coalesce(a.normalized_expression, '')),
			array_remove(array_agg(distinct c.license_declared_raw), null), array_agg(distinct c.snapshot_id)
		from page
		join supply_chain_components c on ` + coordinateKey + `=page.key
		join unnest($1::bigint[]) as scope(snapshot_id) on scope.snapshot_id=c.snapshot_id
		left join supply_chain_component_assessments a on a.component_id=c.id
		where ` + occurrenceFilters + `
		group by page.key order by page.key`,
		[]any{filter.Ecosystem, escapeLike(filter.Search), filter.LicenseExpression, filter.AssessmentStatus, filter.AfterKey, filter.Limit}
}

// SupplyChainPortfolioComponents lists unique coordinates over the authorized
// latest snapshots with stable keyset ordering.
func (s *Store) SupplyChainPortfolioComponents(ctx context.Context, filter supplychain.PortfolioFilter) ([]supplychain.PortfolioComponent, error) {
	result := []supplychain.PortfolioComponent{}
	if len(filter.RepositoryIDs) == 0 {
		return result, nil
	}
	scope, err := s.supplyChainScope(ctx, filter.RepositoryIDs, filter.StreamKey)
	if err != nil {
		return nil, err
	}
	query, arguments := portfolioComponentsQuery(filter)
	rows, err := s.pool.Query(ctx, query, append([]any{customPlan, scope.snapshots}, arguments...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item supplychain.PortfolioComponent
		var snapshots []int64
		if err := rows.Scan(&item.Key, &item.Ecosystem, &item.Namespace, &item.Name, &item.Version, &item.PURL, &item.OccurrenceCount, &item.AssessmentStatuses, &item.Expression,
			&item.DeclaredRaw, &snapshots); err != nil {
			return nil, err
		}
		if item.AssessmentStatuses == nil {
			item.AssessmentStatuses = []string{}
		}
		if item.DeclaredRaw == nil {
			item.DeclaredRaw = []string{}
		}
		// A repository has one latest snapshot per stream, so the snapshots
		// of a coordinate are its repositories.
		item.RepositoryCount, item.RepositoryGitHubIDs = len(snapshots), scope.repositories(snapshots)
		for index, snapshot := range snapshots {
			collected := scope.bySnapshot[snapshot].collectedAt
			if index == 0 || collected.After(item.NewestCollectedAt) {
				item.NewestCollectedAt = collected
			}
			if index == 0 || collected.Before(item.OldestCollectedAt) {
				item.OldestCollectedAt = collected
			}
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// SupplyChainDependencyGraph returns the packages (coordinates without their
// version) that at least filter.MinRepositories repositories of the scope
// use, most shared first, and how many packages qualify in total.
func (s *Store) SupplyChainDependencyGraph(ctx context.Context, filter supplychain.GraphFilter) ([]supplychain.GraphDependency, int, error) {
	result := []supplychain.GraphDependency{}
	if len(filter.RepositoryIDs) == 0 {
		return result, 0, nil
	}
	scope, err := s.supplyChainScope(ctx, filter.RepositoryIDs, filter.StreamKey)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, portfolioGraphQuery, customPlan, scope.snapshots, filter.Ecosystem, escapeLike(filter.Search), "", filter.AssessmentStatus, filter.MinRepositories, filter.Limit)
	if err != nil {
		return nil, 0, err
	}
	matching := 0
	var item supplychain.GraphDependency
	var snapshots []int64
	_, err = pgx.ForEachRow(rows, []any{&item.Ecosystem, &item.Namespace, &item.Name, &item.RepositoryCount, &matching, &item.Versions, &snapshots, &item.AssessmentStatuses, &item.Expressions}, func() error {
		item.RepositoryGitHubIDs = scope.repositories(snapshots)
		result = append(result, item)
		item, snapshots = supplychain.GraphDependency{}, nil
		return nil
	})
	return result, matching, err
}

// PortfolioFacets returns bounded facet counts for the portfolio filters
// within the authorized scope (ecosystems, assessment statuses, expressions).
func (s *Store) SupplyChainPortfolioFacets(ctx context.Context, repositoryIDs []int64, streamKey string) (map[string][]supplychain.FacetCount, error) {
	facets := map[string][]supplychain.FacetCount{"ecosystem": {}, "assessment": {}, "license": {}}
	if len(repositoryIDs) == 0 {
		return facets, nil
	}
	scope, err := s.supplyChainScope(ctx, repositoryIDs, streamKey)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `select coalesce(c.ecosystem, ''), count(*) from supply_chain_components c where c.snapshot_id=any($1) group by 1`, customPlan, scope.snapshots)
	ecosystems, err := collectFacets(rows, err)
	if err != nil {
		return nil, err
	}
	assessments, err := s.supplyChainAssessmentCounts(ctx, scope)
	if err != nil {
		return nil, err
	}
	unassessed := 0
	for _, ecosystem := range ecosystems {
		unassessed += ecosystem.Count
	}
	for _, assessment := range assessments {
		unassessed -= assessment.Count
	}
	if unassessed > 0 {
		assessments = append(assessments, supplychain.FacetCount{Value: "unassessed", Count: unassessed})
	}
	rows, err = s.pool.Query(ctx, `select normalized_expression, count(*) from supply_chain_component_assessments
		where snapshot_id=any($1) and normalized_expression<>'' group by 1 order by 2 desc, 1 limit 50`, customPlan, scope.snapshots)
	licenses, err := collectFacets(rows, err)
	if err != nil {
		return nil, err
	}
	facets["ecosystem"], facets["assessment"], facets["license"] = topFacets(ecosystems, 50), topFacets(assessments, 20), licenses
	return facets, nil
}

// SupplyChainCoordinateOccurrences lists where a coordinate appears within
// the authorized set (the "find repositories using a component" query).
func (s *Store) SupplyChainCoordinateOccurrences(ctx context.Context, repositoryIDs []int64, streamKey, ecosystem, namespace, name, version string, limit int) ([]supplychain.PortfolioOccurrence, error) {
	if len(repositoryIDs) == 0 {
		return []supplychain.PortfolioOccurrence{}, nil
	}
	rows, err := s.pool.Query(ctx, `select r.github_id, r.owner || '/' || r.name, snap.id, snap.collected_at, c.element_id, c.is_root, c.license_declared_raw, coalesce(a.status, ''), coalesce(a.normalized_expression, '')
		from supply_chain_streams st
		join supply_chain_snapshots snap on snap.id=st.latest_snapshot_id
		join repositories r on r.id=st.repository_id
		join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
		left join supply_chain_component_assessments a on a.component_id=c.id
		where st.repository_id=any($1) and st.stream_key=$2 and coalesce(c.ecosystem, '')=$3 and coalesce(c.purl_namespace, '')=$4 and coalesce(c.purl_name, c.name)=$5 and coalesce(c.purl_version, c.version, '')=$6
		and `+coordinateKey+`=$3 || chr(1) || $4 || chr(1) || $5 || chr(1) || $6
		order by r.owner, r.name, c.ordinal limit $7`, repositoryIDs, streamKey, ecosystem, namespace, name, version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []supplychain.PortfolioOccurrence{}
	for rows.Next() {
		var item supplychain.PortfolioOccurrence
		var isRoot bool
		if err := rows.Scan(&item.RepositoryGitHubID, &item.Repository, &item.SnapshotID, &item.CollectedAt, &item.ElementID, &isRoot, &item.DeclaredRaw, &item.AssessmentStatus, &item.Expression); err != nil {
			return nil, err
		}
		if isRoot {
			item.Scope = "root"
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// SnapshotComponentKeys returns the element-level coordinate set of a
// snapshot for comparison: element_id -> (coordinate key, declared license).
func (s *Store) SnapshotComponentKeys(ctx context.Context, snapshotID int64) (map[string][2]string, error) {
	rows, err := s.pool.Query(ctx, `select element_id, coalesce(ecosystem, '') || chr(1) || coalesce(purl_namespace, '') || chr(1) || coalesce(purl_name, name) || chr(1) || coalesce(purl_version, version, ''),
		coalesce(license_declared_raw, '') from supply_chain_components where snapshot_id=$1`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][2]string{}
	for rows.Next() {
		var element, key, declared string
		if err := rows.Scan(&element, &key, &declared); err != nil {
			return nil, err
		}
		result[element] = [2]string{key, declared}
	}
	return result, rows.Err()
}

// SnapshotEdgeKeys returns a snapshot's resolved edges as "from\x01type\x01to".
func (s *Store) SnapshotEdgeKeys(ctx context.Context, snapshotID int64) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `select from_element || chr(1) || relationship || chr(1) || to_element from supply_chain_relationships where snapshot_id=$1 and resolved`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		result[key] = true
	}
	return result, rows.Err()
}

// SupplyChainExportRows streams a snapshot's components with assessments for
// CSV export. Bounded by the snapshot's own size.
func (s *Store) SupplyChainExportRows(ctx context.Context, snapshotID int64) ([]supplychain.ExportRow, error) {
	rows, err := s.pool.Query(ctx, `select c.element_id, c.name, coalesce(c.version, ''), coalesce(c.purl, ''), coalesce(c.ecosystem, ''), coalesce(c.license_declared_raw, ''), coalesce(c.license_concluded_raw, ''),
			c.is_root, coalesce(a.status, ''), coalesce(a.normalized_expression, '')
		from supply_chain_components c left join supply_chain_component_assessments a on a.component_id=c.id where c.snapshot_id=$1 order by c.ordinal`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []supplychain.ExportRow{}
	for rows.Next() {
		var row supplychain.ExportRow
		if err := rows.Scan(&row.ElementID, &row.Name, &row.Version, &row.PURL, &row.Ecosystem, &row.DeclaredRaw, &row.ConcludedRaw, &row.IsRoot, &row.AssessmentStatus, &row.Expression); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
