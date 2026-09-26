// Package review records human license conclusions, policy evaluation
// results, and scoped usage decisions. The three are separate records:
// a conclusion corrects evidence, an evaluation applies a policy version,
// and a decision approves or rejects usage in one repository (or grants an
// expiring exception). Prior records are superseded, never edited, and every
// decision carries the evidence fingerprint and policy version it was made
// against so a later change is detectable.
package review

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/supplychain/license"
	"github.com/balcsida/graphnest/internal/supplychain/policy"
	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
	"github.com/jackc/pgx/v5"
)

var (
	ErrForbidden      = errors.New("forbidden")
	ErrInvalidRequest = errors.New("invalid_request")
	ErrNotFound       = errors.New("not_found")
	// ErrStaleBasis: the caller's evidence fingerprint no longer matches the
	// current assessment, so the reviewer would decide on changed evidence.
	ErrStaleBasis = errors.New("stale_basis")
)

type DecisionKind string

const (
	DecisionApprove   DecisionKind = "approve"
	DecisionReject    DecisionKind = "reject"
	DecisionException DecisionKind = "exception"
)

type Conclusion struct {
	ID               int64
	Coordinates      license.Coordinates
	EvidenceID       int64
	BasisFingerprint []byte
	Reviewer         string
	Reason           string
	CreatedAt        time.Time
	SupersededBy     *int64
}

type PolicyResult struct {
	ComponentID         int64
	SnapshotID          int64
	PolicyID            int64
	EvidenceFingerprint []byte
	Verdict             policy.Verdict
	Explanation         string
	EvaluatedAt         time.Time
}

type EvaluationTarget struct {
	ComponentID         int64
	SnapshotID          int64
	AssessmentStatus    license.AssessmentStatus
	Expression          string
	EvidenceFingerprint []byte
}

type Decision struct {
	ID                  int64
	RepositoryID        int64
	Coordinates         license.Coordinates
	Kind                DecisionKind
	PolicyID            *int64
	PolicyVerdict       policy.Verdict
	EvidenceFingerprint []byte
	Reviewer            string
	Reason              string
	UsageContext        string
	ExpiresAt           *time.Time
	CreatedAt           time.Time
	SupersededBy        *int64
}

type QueueItem struct {
	ComponentID         int64
	SnapshotID          int64
	RepositoryGitHubID  int64
	Repository          string
	ElementID           string
	Name                string
	Version             string
	PURL                string
	Coordinates         license.Coordinates
	AssessmentStatus    license.AssessmentStatus
	Expression          string
	EvidenceFingerprint []byte
	Verdict             policy.Verdict
	Explanation         string
	PolicyID            *int64
	StaleDecisionID     *int64
	Reason              string
}

type Event struct {
	ID           int64
	Kind         string
	Actor        string
	RepositoryID *int64
	Target       string
	Detail       map[string]any
	CreatedAt    time.Time
}

// Store is the persistence the review service needs.
type Store interface {
	CreatePolicy(context.Context, policy.Policy, bool) (policy.Policy, error)
	ActivePolicy(context.Context) (policy.Policy, error)
	Policy(context.Context, int64) (policy.Policy, error)
	Policies(context.Context, int) ([]policy.Policy, error)
	UpsertPolicyResult(context.Context, PolicyResult) error
	PolicyResults(context.Context, int64, []int64) (map[int64]PolicyResult, error)
	PolicyResultHistory(context.Context, int64, int) ([]PolicyResult, error)
	ComponentsNeedingEvaluation(context.Context, int64, int) ([]EvaluationTarget, error)
	ReviewAllowed(context.Context, int64, string) (bool, error)
	SetReviewGrant(context.Context, int64, string, string, bool) error
	RecordConclusion(context.Context, Conclusion, license.Evidence) (Conclusion, error)
	ConclusionHistory(context.Context, license.Coordinates, int) ([]Conclusion, error)
	RecordDecision(context.Context, Decision) (Decision, error)
	CurrentDecision(context.Context, int64, license.Coordinates) (Decision, bool, error)
	DecisionHistory(context.Context, int64, license.Coordinates, int) ([]Decision, error)
	ReviewQueue(context.Context, []int64, string, time.Time, int64, int) ([]QueueItem, error)
	RecordReviewEvent(context.Context, string, string, *int64, string, map[string]any) error
	ReviewEvents(context.Context, []int64, int) ([]Event, error)
	// Assessment plumbing shared with the license worker.
	ComponentsForCoordinates(context.Context, license.Coordinates, int) ([][2]int64, error)
	ComponentDeclarations(context.Context, int64) (*string, *string, error)
	LatestLicenseEvidence(context.Context, license.Coordinates) ([]license.Evidence, error)
	UpsertAssessment(context.Context, license.Assessment) error
	SupplyChainAssessments(context.Context, int64, []int64) (map[int64]license.Assessment, error)
}

// Authorizer resolves repository scope for the live principal.
type Authorizer interface {
	AllAuthorizedRepositories(context.Context, authn.Principal) ([]repository.Repository, error)
	AuthorizedRepository(context.Context, authn.Principal, int64) (repository.Repository, error)
}

// Service is the review application layer.
type Service struct {
	Store      Store
	Authorizer Authorizer
	MaxResults int
	Now        func() time.Time
}

func (service *Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

func (service *Service) maxResults() int {
	if service.MaxResults <= 0 || service.MaxResults > 500 {
		return 100
	}
	return service.MaxResults
}

// canReview: administrators, or principals with a review grant on the
// repository; reviewers must also be able to read the repository (the
// Authorizer check happens first in every caller).
func (service *Service) canReview(ctx context.Context, principal authn.Principal, repositoryID int64) error {
	if principal.Administrator {
		return nil
	}
	allowed, err := service.Store.ReviewAllowed(ctx, repositoryID, principal.Subject)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (service *Service) authorizedRepository(ctx context.Context, principal authn.Principal, githubID int64) (repository.Repository, error) {
	if githubID < 1 {
		return repository.Repository{}, ErrInvalidRequest
	}
	repo, err := service.Authorizer.AuthorizedRepository(ctx, principal, githubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.Repository{}, ErrNotFound
	}
	return repo, err
}

// CreatePolicy stores a new immutable policy version. Policy administration
// is administrator-only (D5 bootstrap; distinct from the review capability).
func (service *Service) CreatePolicy(ctx context.Context, principal authn.Principal, name, description string, rules []byte, unknownHandling policy.Verdict, activate bool) (policy.Policy, error) {
	if !principal.Administrator {
		return policy.Policy{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 || len(description) > 4096 || len(rules) > 64<<10 {
		return policy.Policy{}, ErrInvalidRequest
	}
	if unknownHandling != policy.VerdictReviewRequired && unknownHandling != policy.VerdictProhibited {
		return policy.Policy{}, ErrInvalidRequest
	}
	parsed, err := policy.ParseRules(rules)
	if err != nil {
		return policy.Policy{}, errors.Join(ErrInvalidRequest, err)
	}
	created, err := service.Store.CreatePolicy(ctx, policy.Policy{Name: name, Kind: "organization", Rules: parsed, UnknownHandling: unknownHandling, Description: description, CreatedBy: principal.Subject}, activate)
	if err != nil {
		return policy.Policy{}, err
	}
	_ = service.Store.RecordReviewEvent(ctx, "policy_created", principal.Subject, nil, name+" v"+itoa(created.Version), map[string]any{"policy_id": created.ID, "active": activate, "unknown_handling": string(unknownHandling)})
	return created, nil
}

// InstallExamplePolicy stores the clearly labelled example fixture without
// activating it, so an operator can inspect it before authoring their own.
func (service *Service) InstallExamplePolicy(ctx context.Context, principal authn.Principal, activate bool) (policy.Policy, error) {
	if !principal.Administrator {
		return policy.Policy{}, ErrForbidden
	}
	example := policy.Example()
	example.CreatedBy = principal.Subject
	created, err := service.Store.CreatePolicy(ctx, example, activate)
	if err != nil {
		return policy.Policy{}, err
	}
	_ = service.Store.RecordReviewEvent(ctx, "policy_created", principal.Subject, nil, example.Name+" v"+itoa(created.Version), map[string]any{"policy_id": created.ID, "active": activate, "kind": "example"})
	return created, nil
}

func (service *Service) Policies(ctx context.Context, principal authn.Principal) ([]policy.Policy, error) {
	return service.Store.Policies(ctx, service.maxResults())
}

// EvaluatePending applies the active policy to components lacking a current
// result for it (or whose evidence changed). Returns how many were evaluated.
// Historical results stay in place as non-current rows.
func (service *Service) EvaluatePending(ctx context.Context, limit int) (int, error) {
	active, err := service.Store.ActivePolicy(ctx)
	if errors.Is(err, policy.ErrNoPolicy) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 500
	}
	targets, err := service.Store.ComponentsNeedingEvaluation(ctx, active.ID, limit)
	if err != nil {
		return 0, err
	}
	for _, target := range targets {
		result := service.evaluate(active, target)
		if err := service.Store.UpsertPolicyResult(ctx, result); err != nil {
			return 0, err
		}
	}
	return len(targets), nil
}

func (service *Service) evaluate(active policy.Policy, target EvaluationTarget) PolicyResult {
	result := PolicyResult{ComponentID: target.ComponentID, SnapshotID: target.SnapshotID, PolicyID: active.ID, EvidenceFingerprint: target.EvidenceFingerprint, EvaluatedAt: service.now()}
	switch target.AssessmentStatus {
	case license.AssessmentResolved, license.AssessmentDeclared:
		parsed := spdxexpr.Parse(target.Expression)
		evaluation := active.Evaluate(parsed.Expression)
		result.Verdict, result.Explanation = evaluation.Verdict, evaluation.Explanation
	case license.AssessmentConflict:
		result.Verdict = policy.VerdictReviewRequired
		result.Explanation = "review_required: evidence sources disagree; resolve the conflict with a human conclusion before applying " + active.Name
	case license.AssessmentUnlicensed:
		result.Verdict = policy.VerdictProhibited
		result.Explanation = "prohibited: the package asserts no license grant (UNLICENSED/NONE)"
	default:
		evaluation := active.Evaluate(nil)
		result.Verdict, result.Explanation = evaluation.Verdict, evaluation.Explanation
	}
	return result
}

// Queue lists occurrences needing review within the caller's scope.
func (service *Service) Queue(ctx context.Context, principal authn.Principal, streamKey string, afterID int64, limit int) ([]QueueItem, bool, error) {
	if streamKey == "" {
		streamKey = "github:source"
	}
	repositories, err := service.Authorizer.AllAuthorizedRepositories(ctx, principal)
	if err != nil {
		return nil, false, err
	}
	ids := make([]int64, 0, len(repositories))
	for _, repo := range repositories {
		ids = append(ids, repo.ID)
	}
	if limit <= 0 || limit > service.maxResults() {
		limit = service.maxResults()
	}
	items, err := service.Store.ReviewQueue(ctx, ids, streamKey, service.now(), afterID, limit+1)
	if err != nil {
		return nil, false, err
	}
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	return items, truncated, nil
}

// ConcludeRequest is a human license conclusion for exact coordinates.
type ConcludeRequest struct {
	RepositoryID     int64 // GitHub ID; scopes the permission check and the reassessment
	Coordinates      license.Coordinates
	Expression       string
	Reason           string
	BasisFingerprint string // hex of the assessment fingerprint the reviewer saw
}

// Conclude records a human conclusion as 'human' evidence and rebuilds the
// assessments of every latest-stream occurrence of the coordinates. The
// conclusion must parse as an SPDX expression (or NONE); the basis
// fingerprint must match the current assessment of an occurrence in the
// named repository, or the reviewer would be concluding on changed evidence.
func (service *Service) Conclude(ctx context.Context, principal authn.Principal, request ConcludeRequest) (Conclusion, error) {
	repo, err := service.authorizedRepository(ctx, principal, request.RepositoryID)
	if err != nil {
		return Conclusion{}, err
	}
	if err := service.canReview(ctx, principal, repo.ID); err != nil {
		return Conclusion{}, err
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" || len(reason) > 4096 || request.Coordinates.Name == "" || request.Coordinates.Version == "" || request.Coordinates.Ecosystem == "" {
		return Conclusion{}, ErrInvalidRequest
	}
	parsed := spdxexpr.Parse(request.Expression)
	if parsed.Status != spdxexpr.StatusParsed && parsed.Status != spdxexpr.StatusNone && parsed.Status != spdxexpr.StatusUnlicensed {
		return Conclusion{}, errors.Join(ErrInvalidRequest, errors.New("conclusion must be a valid SPDX expression with known identifiers, NONE, or UNLICENSED"))
	}
	basis, err := service.currentBasis(ctx, repo.ID, request.Coordinates)
	if err != nil {
		return Conclusion{}, err
	}
	if request.BasisFingerprint == "" || request.BasisFingerprint != hex.EncodeToString(basis) {
		return Conclusion{}, ErrStaleBasis
	}
	evidence := license.Evidence{Source: license.SourceHuman, Coordinates: request.Coordinates, FetchedAt: service.now(), Outcome: license.OutcomeResolved, RawValue: strings.TrimSpace(request.Expression), RawKind: license.RawExpression,
		ParseStatus: parsed.Status, NormalizedExpression: parsed.Normalized, ExpressionTree: parsed.Expression, ResolverVersion: license.ResolverVersion, LicenseListVersion: spdxexpr.ListVersion,
		Detail: map[string]any{"reviewer": principal.Subject, "reason": reason}, Message: "human conclusion by " + principal.Subject}
	conclusion, err := service.Store.RecordConclusion(ctx, Conclusion{Coordinates: request.Coordinates, BasisFingerprint: basis, Reviewer: principal.Subject, Reason: reason}, evidence)
	if err != nil {
		return Conclusion{}, err
	}
	repoID := repo.ID
	_ = service.Store.RecordReviewEvent(ctx, "conclusion_recorded", principal.Subject, &repoID, coordinateLabel(request.Coordinates), map[string]any{"conclusion_id": conclusion.ID, "expression": parsed.Normalized, "basis": request.BasisFingerprint})
	return conclusion, service.reassess(ctx, request.Coordinates)
}

// reassess rebuilds assessments for every occurrence of the coordinates and
// marks their policy results for re-evaluation (the evaluation loop picks up
// fingerprint changes).
func (service *Service) reassess(ctx context.Context, coordinates license.Coordinates) error {
	evidence, err := service.Store.LatestLicenseEvidence(ctx, coordinates)
	if err != nil {
		return err
	}
	occurrences, err := service.Store.ComponentsForCoordinates(ctx, coordinates, 10000)
	if err != nil {
		return err
	}
	for _, occurrence := range occurrences {
		declared, concluded, err := service.Store.ComponentDeclarations(ctx, occurrence[0])
		if err != nil {
			return err
		}
		if err := service.Store.UpsertAssessment(ctx, license.AssessWithHuman(occurrence[0], occurrence[1], declared, concluded, evidence, service.now())); err != nil {
			return err
		}
	}
	return nil
}

// currentBasis returns the assessment fingerprint of the coordinates' latest
// occurrence in the repository (ErrNotFound when none exists).
func (service *Service) currentBasis(ctx context.Context, repositoryID int64, coordinates license.Coordinates) ([]byte, error) {
	occurrences, err := service.Store.ComponentsForCoordinates(ctx, coordinates, 10000)
	if err != nil {
		return nil, err
	}
	for _, occurrence := range occurrences {
		assessments, err := service.Store.SupplyChainAssessments(ctx, occurrence[1], []int64{occurrence[0]})
		if err != nil {
			return nil, err
		}
		assessment, ok := assessments[occurrence[0]]
		if !ok {
			continue
		}
		inRepository, err := service.occurrenceInRepository(ctx, occurrence[1], repositoryID)
		if err != nil {
			return nil, err
		}
		if inRepository {
			return assessment.EvidenceFingerprint, nil
		}
	}
	return nil, ErrNotFound
}

// SnapshotRepository is optionally implemented by the store to map a
// snapshot to its repository.
type SnapshotRepository interface {
	SnapshotRepositoryID(context.Context, int64) (int64, error)
}

func (service *Service) occurrenceInRepository(ctx context.Context, snapshotID, repositoryID int64) (bool, error) {
	lookup, ok := service.Store.(SnapshotRepository)
	if !ok {
		return true, nil
	}
	owner, err := lookup.SnapshotRepositoryID(ctx, snapshotID)
	if err != nil {
		return false, err
	}
	return owner == repositoryID, nil
}

// DecideRequest is a scoped usage decision.
type DecideRequest struct {
	RepositoryID     int64
	Coordinates      license.Coordinates
	Kind             DecisionKind
	Reason           string
	UsageContext     string
	ExpiresAt        *time.Time
	BasisFingerprint string
}

// Decide records an approve/reject/exception for exact coordinates in one
// repository. It requires the current assessment fingerprint (optimistic
// concurrency) and records the active policy version and verdict alongside;
// an exception never erases a prohibited verdict or rewrites the license.
func (service *Service) Decide(ctx context.Context, principal authn.Principal, request DecideRequest) (Decision, error) {
	repo, err := service.authorizedRepository(ctx, principal, request.RepositoryID)
	if err != nil {
		return Decision{}, err
	}
	if err := service.canReview(ctx, principal, repo.ID); err != nil {
		return Decision{}, err
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" || len(reason) > 4096 || len(request.UsageContext) > 1024 || request.Coordinates.Name == "" || request.Coordinates.Version == "" || request.Coordinates.Ecosystem == "" {
		return Decision{}, ErrInvalidRequest
	}
	switch request.Kind {
	case DecisionApprove, DecisionReject:
		if request.ExpiresAt != nil {
			return Decision{}, ErrInvalidRequest
		}
	case DecisionException:
		if request.ExpiresAt == nil || !request.ExpiresAt.After(service.now()) || request.ExpiresAt.After(service.now().Add(366*24*time.Hour)) {
			return Decision{}, errors.Join(ErrInvalidRequest, errors.New("an exception needs an expiry within one year"))
		}
	default:
		return Decision{}, ErrInvalidRequest
	}
	basis, err := service.currentBasis(ctx, repo.ID, request.Coordinates)
	if err != nil {
		return Decision{}, err
	}
	if request.BasisFingerprint == "" || request.BasisFingerprint != hex.EncodeToString(basis) {
		return Decision{}, ErrStaleBasis
	}
	decision := Decision{RepositoryID: repo.ID, Coordinates: request.Coordinates, Kind: request.Kind, EvidenceFingerprint: basis, Reviewer: principal.Subject, Reason: reason, UsageContext: strings.TrimSpace(request.UsageContext), ExpiresAt: request.ExpiresAt}
	if active, err := service.Store.ActivePolicy(ctx); err == nil {
		decision.PolicyID = &active.ID
		if verdict, ok := service.currentVerdict(ctx, repo.ID, request.Coordinates); ok {
			decision.PolicyVerdict = verdict
		}
	} else if !errors.Is(err, policy.ErrNoPolicy) {
		return Decision{}, err
	}
	recorded, err := service.Store.RecordDecision(ctx, decision)
	if err != nil {
		return Decision{}, err
	}
	repoID := repo.ID
	detail := map[string]any{"decision_id": recorded.ID, "kind": string(request.Kind), "basis": request.BasisFingerprint, "policy_verdict": string(decision.PolicyVerdict)}
	if request.ExpiresAt != nil {
		detail["expires_at"] = request.ExpiresAt.UTC().Format(time.RFC3339)
	}
	_ = service.Store.RecordReviewEvent(ctx, "decision_recorded", principal.Subject, &repoID, coordinateLabel(request.Coordinates), detail)
	return recorded, nil
}

func (service *Service) currentVerdict(ctx context.Context, repositoryID int64, coordinates license.Coordinates) (policy.Verdict, bool) {
	occurrences, err := service.Store.ComponentsForCoordinates(ctx, coordinates, 10000)
	if err != nil {
		return "", false
	}
	for _, occurrence := range occurrences {
		inRepository, err := service.occurrenceInRepository(ctx, occurrence[1], repositoryID)
		if err != nil || !inRepository {
			continue
		}
		results, err := service.Store.PolicyResults(ctx, occurrence[1], []int64{occurrence[0]})
		if err != nil {
			return "", false
		}
		if result, ok := results[occurrence[0]]; ok {
			return result.Verdict, true
		}
	}
	return "", false
}

// History returns conclusions, decisions, and policy results for coordinates
// in one authorized repository.
type History struct {
	Conclusions   []Conclusion
	Decisions     []Decision
	Current       *Decision
	CurrentStale  bool
	StaleReason   string
	PolicyResults []PolicyResult
	Basis         string
}

func (service *Service) History(ctx context.Context, principal authn.Principal, githubID int64, coordinates license.Coordinates) (History, error) {
	repo, err := service.authorizedRepository(ctx, principal, githubID)
	if err != nil {
		return History{}, err
	}
	if coordinates.Name == "" || coordinates.Ecosystem == "" {
		return History{}, ErrInvalidRequest
	}
	history := History{Conclusions: []Conclusion{}, Decisions: []Decision{}, PolicyResults: []PolicyResult{}}
	history.Conclusions, err = service.Store.ConclusionHistory(ctx, coordinates, service.maxResults())
	if err != nil {
		return History{}, err
	}
	history.Decisions, err = service.Store.DecisionHistory(ctx, repo.ID, coordinates, service.maxResults())
	if err != nil {
		return History{}, err
	}
	if current, ok, err := service.Store.CurrentDecision(ctx, repo.ID, coordinates); err != nil {
		return History{}, err
	} else if ok {
		history.Current = &current
		if current.ExpiresAt != nil && !current.ExpiresAt.After(service.now()) {
			history.CurrentStale, history.StaleReason = true, "exception expired"
		}
	}
	basis, err := service.currentBasis(ctx, repo.ID, coordinates)
	if err == nil {
		history.Basis = hex.EncodeToString(basis)
		if history.Current != nil && string(history.Current.EvidenceFingerprint) != string(basis) {
			history.CurrentStale, history.StaleReason = true, "evidence changed since the decision"
		}
		occurrences, err := service.Store.ComponentsForCoordinates(ctx, coordinates, 10000)
		if err != nil {
			return History{}, err
		}
		for _, occurrence := range occurrences {
			if inRepository, err := service.occurrenceInRepository(ctx, occurrence[1], repo.ID); err == nil && inRepository {
				history.PolicyResults, err = service.Store.PolicyResultHistory(ctx, occurrence[0], service.maxResults())
				if err != nil {
					return History{}, err
				}
				break
			}
		}
	} else if !errors.Is(err, ErrNotFound) {
		return History{}, err
	}
	return history, nil
}

// SetReviewGrant is administrator-only; reviewers still need read access to
// the repository, enforced at use.
func (service *Service) SetReviewGrant(ctx context.Context, principal authn.Principal, githubID int64, subject string, allow bool) error {
	if !principal.Administrator {
		return ErrForbidden
	}
	if subject == "" || len(subject) > 256 {
		return ErrInvalidRequest
	}
	repo, err := service.authorizedRepository(ctx, principal, githubID)
	if err != nil {
		return err
	}
	if err := service.Store.SetReviewGrant(ctx, repo.ID, subject, principal.Subject, allow); err != nil {
		return err
	}
	repoID := repo.ID
	return service.Store.RecordReviewEvent(ctx, "review_grant_changed", principal.Subject, &repoID, subject, map[string]any{"allow": allow})
}

func (service *Service) Events(ctx context.Context, principal authn.Principal) ([]Event, error) {
	repositories, err := service.Authorizer.AllAuthorizedRepositories(ctx, principal)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(repositories))
	for _, repo := range repositories {
		ids = append(ids, repo.ID)
	}
	return service.Store.ReviewEvents(ctx, ids, service.maxResults())
}

func coordinateLabel(coordinates license.Coordinates) string {
	label := coordinates.Ecosystem + ":" + coordinates.Name + "@" + coordinates.Version
	if coordinates.Namespace != "" {
		label = coordinates.Ecosystem + ":" + coordinates.Namespace + "/" + coordinates.Name + "@" + coordinates.Version
	}
	return label
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
