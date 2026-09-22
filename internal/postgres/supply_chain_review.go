package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain/license"
	"github.com/balcsida/graphnest/internal/supplychain/policy"
	"github.com/balcsida/graphnest/internal/supplychain/review"
	"github.com/jackc/pgx/v5"
)

// CreatePolicy inserts an immutable policy version. When activate is true it
// deactivates the current active policy in the same transaction.
func (s *Store) CreatePolicy(ctx context.Context, item policy.Policy, activate bool) (policy.Policy, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return policy.Policy{}, err
	}
	defer tx.Rollback(ctx)
	rules, err := json.Marshal(item.Rules)
	if err != nil {
		return policy.Policy{}, err
	}
	var version int
	if err := tx.QueryRow(ctx, `select coalesce(max(version), 0)+1 from supply_chain_policies where name=$1`, item.Name).Scan(&version); err != nil {
		return policy.Policy{}, err
	}
	if activate {
		if _, err := tx.Exec(ctx, `update supply_chain_policies set active=false where active`); err != nil {
			return policy.Policy{}, err
		}
	}
	item.Version, item.Active = version, activate
	if err := tx.QueryRow(ctx, `insert into supply_chain_policies (name, version, kind, rules, unknown_handling, description, created_by, active) values ($1, $2, $3, $4, $5, $6, $7, $8) returning id`,
		item.Name, version, item.Kind, rules, string(item.UnknownHandling), item.Description, item.CreatedBy, activate).Scan(&item.ID); err != nil {
		return policy.Policy{}, err
	}
	return item, tx.Commit(ctx)
}

const policyColumns = `id, name, version, kind, rules, unknown_handling, description, created_by, active`

func scanPolicy(row interface{ Scan(...any) error }) (policy.Policy, error) {
	var item policy.Policy
	var rules []byte
	var unknown string
	if err := row.Scan(&item.ID, &item.Name, &item.Version, &item.Kind, &rules, &unknown, &item.Description, &item.CreatedBy, &item.Active); err != nil {
		return policy.Policy{}, err
	}
	item.UnknownHandling = policy.Verdict(unknown)
	if err := json.Unmarshal(rules, &item.Rules); err != nil {
		return policy.Policy{}, err
	}
	return item, nil
}

// ActivePolicy returns the active policy or policy.ErrNoPolicy.
func (s *Store) ActivePolicy(ctx context.Context) (policy.Policy, error) {
	item, err := scanPolicy(s.pool.QueryRow(ctx, `select `+policyColumns+` from supply_chain_policies where active`))
	if errors.Is(err, pgx.ErrNoRows) {
		return policy.Policy{}, policy.ErrNoPolicy
	}
	return item, err
}

func (s *Store) Policy(ctx context.Context, id int64) (policy.Policy, error) {
	return scanPolicy(s.pool.QueryRow(ctx, `select `+policyColumns+` from supply_chain_policies where id=$1`, id))
}

func (s *Store) Policies(ctx context.Context, limit int) ([]policy.Policy, error) {
	rows, err := s.pool.Query(ctx, `select `+policyColumns+` from supply_chain_policies order by id desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	policies := []policy.Policy{}
	for rows.Next() {
		item, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, item)
	}
	return policies, rows.Err()
}

// UpsertPolicyResult records a current evaluation for a component, retiring
// the previous current row rather than editing it.
func (s *Store) UpsertPolicyResult(ctx context.Context, result review.PolicyResult) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `update supply_chain_policy_results set current=false where component_id=$1 and current`, result.ComponentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into supply_chain_policy_results (component_id, snapshot_id, policy_id, evidence_fingerprint, verdict, explanation, evaluated_at, current) values ($1, $2, $3, $4, $5, $6, $7, true)`,
		result.ComponentID, result.SnapshotID, result.PolicyID, result.EvidenceFingerprint, string(result.Verdict), result.Explanation, result.EvaluatedAt.UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PolicyResults returns current results for a snapshot's components.
func (s *Store) PolicyResults(ctx context.Context, snapshotID int64, componentIDs []int64) (map[int64]review.PolicyResult, error) {
	rows, err := s.pool.Query(ctx, `select component_id, snapshot_id, policy_id, evidence_fingerprint, verdict, explanation, evaluated_at from supply_chain_policy_results
		where snapshot_id=$1 and component_id=any($2) and current`, snapshotID, componentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := map[int64]review.PolicyResult{}
	for rows.Next() {
		var result review.PolicyResult
		var verdict string
		if err := rows.Scan(&result.ComponentID, &result.SnapshotID, &result.PolicyID, &result.EvidenceFingerprint, &verdict, &result.Explanation, &result.EvaluatedAt); err != nil {
			return nil, err
		}
		result.Verdict = policy.Verdict(verdict)
		results[result.ComponentID] = result
	}
	return results, rows.Err()
}

// PolicyResultHistory lists every evaluation of one component, newest first.
func (s *Store) PolicyResultHistory(ctx context.Context, componentID int64, limit int) ([]review.PolicyResult, error) {
	rows, err := s.pool.Query(ctx, `select component_id, snapshot_id, policy_id, evidence_fingerprint, verdict, explanation, evaluated_at from supply_chain_policy_results
		where component_id=$1 order by id desc limit $2`, componentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []review.PolicyResult{}
	for rows.Next() {
		var result review.PolicyResult
		var verdict string
		if err := rows.Scan(&result.ComponentID, &result.SnapshotID, &result.PolicyID, &result.EvidenceFingerprint, &verdict, &result.Explanation, &result.EvaluatedAt); err != nil {
			return nil, err
		}
		result.Verdict = policy.Verdict(verdict)
		results = append(results, result)
	}
	return results, rows.Err()
}

// ComponentsForPolicyEvaluation lists latest-stream components (with their
// assessments) for the authorized repositories that lack a current result
// for the given policy or whose result fingerprint differs from the
// assessment fingerprint. Bounded.
func (s *Store) ComponentsNeedingEvaluation(ctx context.Context, policyID int64, limit int) ([]review.EvaluationTarget, error) {
	rows, err := s.pool.Query(ctx, `select c.id, c.snapshot_id, a.status, a.normalized_expression, a.evidence_fingerprint
		from supply_chain_streams st
		join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
		left join supply_chain_component_assessments a on a.component_id=c.id
		left join supply_chain_policy_results r on r.component_id=c.id and r.current
		where r.id is null or r.policy_id<>$1 or coalesce(r.evidence_fingerprint, '') <> coalesce(a.evidence_fingerprint, '')
		order by c.id limit $2`, policyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []review.EvaluationTarget{}
	for rows.Next() {
		var target review.EvaluationTarget
		var status, expression *string
		if err := rows.Scan(&target.ComponentID, &target.SnapshotID, &status, &expression, &target.EvidenceFingerprint); err != nil {
			return nil, err
		}
		if status != nil {
			target.AssessmentStatus = license.AssessmentStatus(*status)
		}
		if expression != nil {
			target.Expression = *expression
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// ReviewAllowed reports whether a subject holds a review grant for the repository.
func (s *Store) ReviewAllowed(ctx context.Context, repositoryID int64, subject string) (bool, error) {
	var allowed bool
	err := s.pool.QueryRow(ctx, `select exists(select 1 from supply_chain_review_grants where repository_id=$1 and subject=$2)`, repositoryID, subject).Scan(&allowed)
	return allowed, err
}

func (s *Store) SetReviewGrant(ctx context.Context, repositoryID int64, subject, grantedBy string, allow bool) error {
	if !allow {
		_, err := s.pool.Exec(ctx, `delete from supply_chain_review_grants where repository_id=$1 and subject=$2`, repositoryID, subject)
		return err
	}
	_, err := s.pool.Exec(ctx, `insert into supply_chain_review_grants (repository_id, subject, granted_by) values ($1, $2, $3) on conflict do nothing`, repositoryID, subject, grantedBy)
	return err
}

// RecordConclusion stores a human license conclusion as evidence plus its
// linkage, superseding the previous conclusion for the coordinates.
func (s *Store) RecordConclusion(ctx context.Context, conclusion review.Conclusion, evidence license.Evidence) (review.Conclusion, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return review.Conclusion{}, err
	}
	defer tx.Rollback(ctx)
	var tree []byte
	if evidence.ExpressionTree != nil {
		if tree, err = json.Marshal(evidence.ExpressionTree); err != nil {
			return review.Conclusion{}, err
		}
	}
	detail, err := json.Marshal(nonNilAny(evidence.Detail))
	if err != nil {
		return review.Conclusion{}, err
	}
	unknown := evidence.UnknownTerms
	if unknown == nil {
		unknown = []string{}
	}
	var evidenceID int64
	if err := tx.QueryRow(ctx, `insert into supply_chain_license_evidence (source, route, ecosystem, namespace, name, version, raw_value, raw_kind, parse_status, normalized_expression, expression_tree, unknown_terms,
			detail, resolver_version, license_list_version, fetched_at, outcome, message)
		values ('human', '', $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'resolved', $15) returning id`,
		conclusion.Coordinates.Ecosystem, conclusion.Coordinates.Namespace, conclusion.Coordinates.Name, conclusion.Coordinates.Version, evidence.RawValue, string(evidence.RawKind), string(evidence.ParseStatus),
		evidence.NormalizedExpression, nullableJSON(tree), unknown, detail, evidence.ResolverVersion, evidence.LicenseListVersion, evidence.FetchedAt.UTC(), evidence.Message).Scan(&evidenceID); err != nil {
		return review.Conclusion{}, err
	}
	conclusion.EvidenceID = evidenceID
	if err := tx.QueryRow(ctx, `insert into supply_chain_conclusions (ecosystem, namespace, name, version, evidence_id, evidence_fingerprint, reviewer, reason) values ($1, $2, $3, $4, $5, $6, $7, $8) returning id, created_at`,
		conclusion.Coordinates.Ecosystem, conclusion.Coordinates.Namespace, conclusion.Coordinates.Name, conclusion.Coordinates.Version, evidenceID, conclusion.BasisFingerprint, conclusion.Reviewer, conclusion.Reason).Scan(&conclusion.ID, &conclusion.CreatedAt); err != nil {
		return review.Conclusion{}, err
	}
	if _, err := tx.Exec(ctx, `update supply_chain_conclusions set superseded_by=$1 where ecosystem=$2 and namespace=$3 and name=$4 and version=$5 and id<>$1 and superseded_by is null`,
		conclusion.ID, conclusion.Coordinates.Ecosystem, conclusion.Coordinates.Namespace, conclusion.Coordinates.Name, conclusion.Coordinates.Version); err != nil {
		return review.Conclusion{}, err
	}
	return conclusion, tx.Commit(ctx)
}

// RecordDecision stores a scoped decision and supersedes the previous
// current decision for the same scope.
func (s *Store) RecordDecision(ctx context.Context, decision review.Decision) (review.Decision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return review.Decision{}, err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `insert into supply_chain_decisions (repository_id, ecosystem, namespace, name, version, kind, policy_id, policy_verdict, evidence_fingerprint, reviewer, reason, usage_context, expires_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) returning id, created_at`,
		decision.RepositoryID, decision.Coordinates.Ecosystem, decision.Coordinates.Namespace, decision.Coordinates.Name, decision.Coordinates.Version, string(decision.Kind), decision.PolicyID, string(decision.PolicyVerdict),
		decision.EvidenceFingerprint, decision.Reviewer, decision.Reason, decision.UsageContext, decision.ExpiresAt).Scan(&decision.ID, &decision.CreatedAt); err != nil {
		return review.Decision{}, err
	}
	if _, err := tx.Exec(ctx, `update supply_chain_decisions set superseded_by=$1 where repository_id=$2 and ecosystem=$3 and namespace=$4 and name=$5 and version=$6 and id<>$1 and superseded_by is null`,
		decision.ID, decision.RepositoryID, decision.Coordinates.Ecosystem, decision.Coordinates.Namespace, decision.Coordinates.Name, decision.Coordinates.Version); err != nil {
		return review.Decision{}, err
	}
	return decision, tx.Commit(ctx)
}

const decisionColumns = `id, repository_id, ecosystem, namespace, name, version, kind, policy_id, policy_verdict, evidence_fingerprint, reviewer, reason, usage_context, expires_at, created_at, superseded_by`

func scanDecision(row interface{ Scan(...any) error }) (review.Decision, error) {
	var decision review.Decision
	var kind, verdict string
	if err := row.Scan(&decision.ID, &decision.RepositoryID, &decision.Coordinates.Ecosystem, &decision.Coordinates.Namespace, &decision.Coordinates.Name, &decision.Coordinates.Version, &kind, &decision.PolicyID, &verdict,
		&decision.EvidenceFingerprint, &decision.Reviewer, &decision.Reason, &decision.UsageContext, &decision.ExpiresAt, &decision.CreatedAt, &decision.SupersededBy); err != nil {
		return review.Decision{}, err
	}
	decision.Kind, decision.PolicyVerdict = review.DecisionKind(kind), policy.Verdict(verdict)
	return decision, nil
}

// CurrentDecision returns the non-superseded decision for a scope, if any.
func (s *Store) CurrentDecision(ctx context.Context, repositoryID int64, coordinates license.Coordinates) (review.Decision, bool, error) {
	decision, err := scanDecision(s.pool.QueryRow(ctx, `select `+decisionColumns+` from supply_chain_decisions where repository_id=$1 and ecosystem=$2 and namespace=$3 and name=$4 and version=$5 and superseded_by is null order by id desc limit 1`,
		repositoryID, coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version))
	if errors.Is(err, pgx.ErrNoRows) {
		return review.Decision{}, false, nil
	}
	return decision, err == nil, err
}

// DecisionHistory lists every decision for a scope, newest first.
func (s *Store) DecisionHistory(ctx context.Context, repositoryID int64, coordinates license.Coordinates, limit int) ([]review.Decision, error) {
	rows, err := s.pool.Query(ctx, `select `+decisionColumns+` from supply_chain_decisions where repository_id=$1 and ecosystem=$2 and namespace=$3 and name=$4 and version=$5 order by id desc limit $6`,
		repositoryID, coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	decisions := []review.Decision{}
	for rows.Next() {
		decision, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, decision)
	}
	return decisions, rows.Err()
}

// ConclusionHistory lists conclusions for coordinates, newest first.
func (s *Store) ConclusionHistory(ctx context.Context, coordinates license.Coordinates, limit int) ([]review.Conclusion, error) {
	rows, err := s.pool.Query(ctx, `select id, ecosystem, namespace, name, version, evidence_id, evidence_fingerprint, reviewer, reason, created_at, superseded_by from supply_chain_conclusions
		where ecosystem=$1 and namespace=$2 and name=$3 and version=$4 order by id desc limit $5`, coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conclusions := []review.Conclusion{}
	for rows.Next() {
		var conclusion review.Conclusion
		if err := rows.Scan(&conclusion.ID, &conclusion.Coordinates.Ecosystem, &conclusion.Coordinates.Namespace, &conclusion.Coordinates.Name, &conclusion.Coordinates.Version, &conclusion.EvidenceID, &conclusion.BasisFingerprint, &conclusion.Reviewer, &conclusion.Reason, &conclusion.CreatedAt, &conclusion.SupersededBy); err != nil {
			return nil, err
		}
		conclusions = append(conclusions, conclusion)
	}
	return conclusions, rows.Err()
}

// ReviewQueue lists latest-stream occurrences in the authorized repositories
// whose current policy verdict is not approved and that have no current,
// unexpired decision, ordered by verdict severity then component. Bounded.
func (s *Store) ReviewQueue(ctx context.Context, repositoryIDs []int64, streamKey string, now time.Time, afterID int64, limit int) ([]review.QueueItem, error) {
	if len(repositoryIDs) == 0 {
		return []review.QueueItem{}, nil
	}
	rows, err := s.pool.Query(ctx, `select c.id, c.snapshot_id, r.github_id, r.owner || '/' || r.name, c.element_id, c.name, coalesce(c.version, ''), coalesce(c.purl, ''), coalesce(c.ecosystem, ''), coalesce(c.purl_namespace, ''), coalesce(c.purl_name, ''), coalesce(c.purl_version, c.version, ''),
			coalesce(a.status, ''), coalesce(a.normalized_expression, ''), a.evidence_fingerprint, coalesce(pr.verdict, ''), coalesce(pr.explanation, ''), pr.policy_id,
			d.id, d.kind, d.expires_at, d.evidence_fingerprint
		from supply_chain_streams st
		join supply_chain_snapshots snap on snap.id=st.latest_snapshot_id
		join repositories r on r.id=st.repository_id
		join supply_chain_components c on c.snapshot_id=st.latest_snapshot_id
		left join supply_chain_component_assessments a on a.component_id=c.id
		left join supply_chain_policy_results pr on pr.component_id=c.id and pr.current
		left join lateral (
			select id, kind, expires_at, evidence_fingerprint from supply_chain_decisions dd
			where dd.repository_id=st.repository_id and dd.ecosystem=coalesce(c.ecosystem, '') and dd.namespace=coalesce(c.purl_namespace, '') and dd.name=coalesce(c.purl_name, '') and dd.version=coalesce(c.purl_version, c.version, '') and dd.superseded_by is null
			order by dd.id desc limit 1
		) d on true
		where st.repository_id=any($1) and st.stream_key=$2 and c.id>$3
		and coalesce(pr.verdict, 'unknown') <> 'approved'
		and (d.id is null or (d.expires_at is not null and d.expires_at <= $4) or (a.evidence_fingerprint is not null and d.evidence_fingerprint <> a.evidence_fingerprint))
		order by c.id limit $5`, repositoryIDs, streamKey, afterID, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []review.QueueItem{}
	for rows.Next() {
		var item review.QueueItem
		var status, expression, verdict, explanation string
		var decisionID *int64
		var decisionKind *string
		var decisionExpires *time.Time
		var decisionFingerprint []byte
		if err := rows.Scan(&item.ComponentID, &item.SnapshotID, &item.RepositoryGitHubID, &item.Repository, &item.ElementID, &item.Name, &item.Version, &item.PURL, &item.Coordinates.Ecosystem, &item.Coordinates.Namespace,
			&item.Coordinates.Name, &item.Coordinates.Version, &status, &expression, &item.EvidenceFingerprint, &verdict, &explanation, &item.PolicyID, &decisionID, &decisionKind, &decisionExpires, &decisionFingerprint); err != nil {
			return nil, err
		}
		item.AssessmentStatus, item.Expression, item.Verdict, item.Explanation = license.AssessmentStatus(status), expression, policy.Verdict(verdict), explanation
		if decisionID != nil {
			item.StaleDecisionID = decisionID
			switch {
			case decisionExpires != nil && !decisionExpires.After(now):
				item.Reason = "exception expired"
			case decisionFingerprint != nil && item.EvidenceFingerprint != nil && string(decisionFingerprint) != string(item.EvidenceFingerprint):
				item.Reason = "evidence changed since the decision"
			}
		} else if item.Verdict == "" {
			item.Reason = "not yet evaluated"
		} else {
			item.Reason = "no decision recorded"
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RecordReviewEvent appends to the audit trail.
func (s *Store) RecordReviewEvent(ctx context.Context, kind, actor string, repositoryID *int64, target string, detail map[string]any) error {
	data, err := json.Marshal(nonNilAny(detail))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `insert into supply_chain_review_events (kind, actor, repository_id, target, detail) values ($1, $2, $3, $4, $5)`, kind, actor, repositoryID, target, data)
	return err
}

// ReviewEvents lists audit events, newest first, optionally for one repository.
func (s *Store) ReviewEvents(ctx context.Context, repositoryIDs []int64, limit int) ([]review.Event, error) {
	rows, err := s.pool.Query(ctx, `select id, kind, actor, repository_id, target, detail, created_at from supply_chain_review_events
		where repository_id is null or repository_id=any($1) order by id desc limit $2`, repositoryIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []review.Event{}
	for rows.Next() {
		var event review.Event
		var detail []byte
		if err := rows.Scan(&event.ID, &event.Kind, &event.Actor, &event.RepositoryID, &event.Target, &detail, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(detail, &event.Detail); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// SnapshotRepositoryID maps a snapshot to its repository row.
func (s *Store) SnapshotRepositoryID(ctx context.Context, snapshotID int64) (int64, error) {
	var repositoryID int64
	err := s.pool.QueryRow(ctx, `select repository_id from supply_chain_snapshots where id=$1`, snapshotID).Scan(&repositoryID)
	return repositoryID, err
}
