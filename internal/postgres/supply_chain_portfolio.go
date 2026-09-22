package postgres

import (
	"context"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain"
)

// Portfolio queries aggregate over the latest snapshot of one stream per
// authorized repository. Every query takes the caller's authorized repository
// IDs and never reads beyond them; totals, facets, and pages are computed
// inside that scope.

// SupplyChainOverview computes the overview for the stream across the
// authorized repositories. staleBefore is the freshness cutoff.
func (s *Store) SupplyChainOverview(ctx context.Context, repositoryIDs []int64, streamKey string, staleBefore time.Time) (supplychain.PortfolioOverview, error) {
	overview := supplychain.PortfolioOverview{Repositories: len(repositoryIDs), AssessmentCounts: map[string]int{}}
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
	if err := s.pool.QueryRow(ctx, `select count(distinct (c.ecosystem, coalesce(c.purl_namespace, ''), coalesce(c.purl_name, c.name), coalesce(c.purl_version, c.version, ''))),
			count(*) filter (where c.purl is null), count(*) filter (where coalesce(c.purl_version, c.version, '')='')
		from supply_chain_streams st join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
		where st.repository_id=any($1) and st.stream_key=$2`, repositoryIDs, streamKey).Scan(&overview.UniqueCoordinates, &overview.WithoutPURL, &overview.WithoutVersion); err != nil {
		return supplychain.PortfolioOverview{}, err
	}
	rows, err := s.pool.Query(ctx, `select coalesce(a.status, 'unassessed'), count(*) from supply_chain_streams st
		join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
		left join supply_chain_component_assessments a on a.component_id=c.id
		where st.repository_id=any($1) and st.stream_key=$2 group by 1`, repositoryIDs, streamKey)
	if err != nil {
		return supplychain.PortfolioOverview{}, err
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return supplychain.PortfolioOverview{}, err
		}
		if status == "unassessed" {
			overview.UnassessedComponent = count
		} else {
			overview.AssessmentCounts[status] = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return supplychain.PortfolioOverview{}, err
	}
	rows, err = s.pool.Query(ctx, `select coalesce(c.ecosystem, ''), count(*) from supply_chain_streams st join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
		where st.repository_id=any($1) and st.stream_key=$2 group by 1 order by 2 desc, 1 limit 50`, repositoryIDs, streamKey)
	if err != nil {
		return supplychain.PortfolioOverview{}, err
	}
	defer rows.Close()
	overview.Ecosystems = []supplychain.FacetCount{}
	for rows.Next() {
		var facet supplychain.FacetCount
		if err := rows.Scan(&facet.Value, &facet.Count); err != nil {
			return supplychain.PortfolioOverview{}, err
		}
		overview.Ecosystems = append(overview.Ecosystems, facet)
	}
	return overview, rows.Err()
}

// SupplyChainPortfolioComponents lists unique coordinates over the authorized
// latest snapshots with stable keyset ordering.
func (s *Store) SupplyChainPortfolioComponents(ctx context.Context, filter supplychain.PortfolioFilter) ([]supplychain.PortfolioComponent, error) {
	if len(filter.RepositoryIDs) == 0 {
		return []supplychain.PortfolioComponent{}, nil
	}
	rows, err := s.pool.Query(ctx, `with occurrences as (
			select c.*, st.repository_id, snap.collected_at, r.github_id, a.status as assessment_status, a.normalized_expression,
				coalesce(c.ecosystem, '') as eco, coalesce(c.purl_namespace, '') as ns, coalesce(c.purl_name, c.name) as pname, coalesce(c.purl_version, c.version, '') as pversion
			from supply_chain_streams st
			join supply_chain_snapshots snap on snap.id=st.latest_snapshot_id
			join repositories r on r.id=st.repository_id
			join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
			left join supply_chain_component_assessments a on a.component_id=c.id
			where st.repository_id=any($1) and st.stream_key=$2
		), grouped as (
			select eco, ns, pname, pversion,
				eco || chr(1) || ns || chr(1) || pname || chr(1) || pversion as key,
				max(coalesce(purl, '')) as purl,
				count(distinct repository_id) as repositories, count(*) as occurrences,
				array_remove(array_agg(distinct assessment_status), null) as statuses,
				max(coalesce(normalized_expression, '')) as expression,
				array_remove(array_agg(distinct license_declared_raw), null) as declared,
				max(collected_at) as newest, min(collected_at) as oldest,
				array_agg(distinct github_id order by github_id) as github_ids
			from occurrences
			where ($3='' or eco=$3)
			and ($4='' or lower(pname) like '%'||lower($4)||'%' or lower(coalesce(purl, '')) like '%'||lower($4)||'%' or lower(name) like '%'||lower($4)||'%')
			and ($5='' or lower(coalesce(normalized_expression, ''))=lower($5))
			and ($6='' or coalesce(assessment_status, 'unassessed')=$6)
			group by eco, ns, pname, pversion
		)
		select key, eco, ns, pname, pversion, purl, repositories, occurrences, statuses, expression, declared, newest, oldest, github_ids
		from grouped where ($7='' or key>$7) order by key limit $8`,
		filter.RepositoryIDs, filter.StreamKey, filter.Ecosystem, escapeLike(filter.Search), filter.LicenseExpression, filter.AssessmentStatus, filter.AfterKey, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []supplychain.PortfolioComponent{}
	for rows.Next() {
		var item supplychain.PortfolioComponent
		if err := rows.Scan(&item.Key, &item.Ecosystem, &item.Namespace, &item.Name, &item.Version, &item.PURL, &item.RepositoryCount, &item.OccurrenceCount, &item.AssessmentStatuses, &item.Expression,
			&item.DeclaredRaw, &item.NewestCollectedAt, &item.OldestCollectedAt, &item.RepositoryGitHubIDs); err != nil {
			return nil, err
		}
		if item.AssessmentStatuses == nil {
			item.AssessmentStatuses = []string{}
		}
		if item.DeclaredRaw == nil {
			item.DeclaredRaw = []string{}
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// PortfolioFacets returns bounded facet counts for the portfolio filters
// within the authorized scope (ecosystems, assessment statuses, expressions).
func (s *Store) SupplyChainPortfolioFacets(ctx context.Context, repositoryIDs []int64, streamKey string) (map[string][]supplychain.FacetCount, error) {
	facets := map[string][]supplychain.FacetCount{"ecosystem": {}, "assessment": {}, "license": {}}
	if len(repositoryIDs) == 0 {
		return facets, nil
	}
	for name, query := range map[string]string{
		"ecosystem": `select coalesce(c.ecosystem, ''), count(*) from supply_chain_streams st join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
			where st.repository_id=any($1) and st.stream_key=$2 group by 1 order by 2 desc, 1 limit 50`,
		"assessment": `select coalesce(a.status, 'unassessed'), count(*) from supply_chain_streams st join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
			left join supply_chain_component_assessments a on a.component_id=c.id where st.repository_id=any($1) and st.stream_key=$2 group by 1 order by 2 desc, 1 limit 20`,
		"license": `select a.normalized_expression, count(*) from supply_chain_streams st join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
			join supply_chain_component_assessments a on a.component_id=c.id where st.repository_id=any($1) and st.stream_key=$2 and a.normalized_expression<>'' group by 1 order by 2 desc, 1 limit 50`,
	} {
		rows, err := s.pool.Query(ctx, query, repositoryIDs, streamKey)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var facet supplychain.FacetCount
			if err := rows.Scan(&facet.Value, &facet.Count); err != nil {
				rows.Close()
				return nil, err
			}
			facets[name] = append(facets[name], facet)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
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
