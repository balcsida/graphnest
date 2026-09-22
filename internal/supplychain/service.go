package supplychain

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

var (
	ErrForbidden      = errors.New("forbidden")
	ErrInvalidRequest = errors.New("invalid_request")
	ErrNotFound       = errors.New("not_found")
	ErrNoInventory    = errors.New("no_inventory")
)

// Store is what the service needs from PostgreSQL. It is satisfied by
// *postgres.Store; the service never receives unauthorized IDs from callers
// without re-scoping them to the authorized repository set.
type Store interface {
	SupplyChainStream(context.Context, int64, string) (Stream, error)
	SupplyChainSnapshot(context.Context, int64, []int64) (Snapshot, error)
	SupplyChainSnapshots(context.Context, int64, string, int64, int) ([]Snapshot, error)
	SupplyChainDocument(context.Context, int64, []int64) ([]byte, string, []byte, error)
	SupplyChainComponents(context.Context, int64, int, int, string) ([]Component, error)
	SupplyChainRelationships(context.Context, int64, string, int) ([]Relationship, error)
	SupplyChainCollections(context.Context, int64, string, int64, int) ([]Collection, error)
	SupplyChainJob(context.Context, int64, []int64) (Job, error)
	SupplyChainActiveJob(context.Context, int64, string) (Job, bool, error)
	EnqueueSupplyChainJob(context.Context, int64, string, string, string, int, time.Time) (Job, bool, error)
}

// Authorizer resolves the live principal's repository scope. *authz.Postgres
// satisfies it (postgres imports this package for its models, so the authz
// package cannot be imported here).
type Authorizer interface {
	AllAuthorizedRepositories(context.Context, authn.Principal) ([]repository.Repository, error)
	AuthorizedRepository(context.Context, authn.Principal, int64) (repository.Repository, error)
}

// Service is the shared inventory read/refresh layer used by REST (and later
// MCP). Every method authorizes the repository with the live principal first
// and only then touches supply-chain rows scoped to that repository.
type Service struct {
	Store      Store
	Authorizer Authorizer
	Interval   time.Duration
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

func (service *Service) interval() time.Duration {
	if service.Interval <= 0 {
		return 24 * time.Hour
	}
	return service.Interval
}

// authorizedRepository resolves a GitHub repository ID to the internal row
// the principal may see. Delegation-only tokens and unknown repositories both
// yield ErrNotFound so the response does not reveal existence.
func (service *Service) authorizedRepository(ctx context.Context, principal authn.Principal, githubID int64) (repository.Repository, error) {
	if githubID < 1 {
		return repository.Repository{}, ErrInvalidRequest
	}
	repo, err := service.Authorizer.AuthorizedRepository(ctx, principal, githubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.Repository{}, ErrNotFound
	}
	if err != nil {
		return repository.Repository{}, err
	}
	return repo, nil
}

// AuthorizedRepositoryIDs returns the internal IDs of every repository the
// principal may read. It is the scope passed to snapshot/job lookups by ID.
func (service *Service) AuthorizedRepositoryIDs(ctx context.Context, principal authn.Principal) ([]int64, error) {
	repositories, err := service.Authorizer.AllAuthorizedRepositories(ctx, principal)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(repositories))
	for _, repo := range repositories {
		ids = append(ids, repo.ID)
	}
	return ids, nil
}

// ValidStream reports whether a stream selector is one the service serves.
// Milestone 1 serves the GitHub source stream only.
func ValidStream(stream string) bool {
	return stream == "" || stream == StreamGitHubSource
}

func normalizeStream(stream string) (string, error) {
	if !ValidStream(stream) {
		return "", ErrInvalidRequest
	}
	if stream == "" {
		return StreamGitHubSource, nil
	}
	return stream, nil
}

func (service *Service) Status(ctx context.Context, principal authn.Principal, githubID int64, streamKey string) (api.SupplyChainRepositoryStatus, error) {
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return api.SupplyChainRepositoryStatus{}, err
	}
	repo, err := service.authorizedRepository(ctx, principal, githubID)
	if err != nil {
		return api.SupplyChainRepositoryStatus{}, err
	}
	status := api.SupplyChainRepositoryStatus{RepositoryID: repo.GitHubID, Repository: repo.Name, Stream: streamKey, Producer: string(ProducerGitHub), Subject: string(SubjectSource),
		Collection: "never", Enrichment: "not_configured", Notes: []string{}, Documents: []api.SupplyChainDocumentRef{}}
	stream, err := service.Store.SupplyChainStream(ctx, repo.ID, streamKey)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return api.SupplyChainRepositoryStatus{}, err
	}
	if err == nil {
		status.OptOut = stream.OptOut
		if stream.LatestSnapshotID != nil {
			snapshot, err := service.Store.SupplyChainSnapshot(ctx, *stream.LatestSnapshotID, []int64{repo.ID})
			if err != nil {
				return api.SupplyChainRepositoryStatus{}, err
			}
			summary := snapshotSummary(snapshot)
			status.LatestSnapshot = &summary
			status.Documents = append(status.Documents, documentRef(snapshot))
			age := int64(service.now().Sub(snapshot.CollectedAt) / time.Second)
			status.FreshnessSeconds = &age
			status.Collection = "current"
			if service.now().Sub(snapshot.CollectedAt) > 2*service.interval() {
				status.Collection = "stale"
			}
		}
		collections, err := service.Store.SupplyChainCollections(ctx, repo.ID, streamKey, 0, 1)
		if err != nil {
			return api.SupplyChainRepositoryStatus{}, err
		}
		if len(collections) == 1 {
			last := collectionSummary(collections[0])
			status.LastCollection = &last
			if !collections[0].Outcome.Success() {
				status.Collection = "failed"
				if status.LatestSnapshot != nil {
					status.Notes = append(status.Notes, "The latest refresh failed; the inventory shown is the last successful observation.")
				}
			}
			if collections[0].ProjectionError != "" {
				status.Notes = append(status.Notes, "The compatibility projection into SCIP package mappings failed for the latest snapshot; the inventory itself is intact.")
			}
		}
	}
	if status.LatestSnapshot != nil {
		status.Notes = append(status.Notes, "GitHub dependency-graph exports are timestamped observations of the default branch; they are not bound to a commit and carry no license data.")
		if status.LatestSnapshot.WarningCount > 0 {
			status.Notes = append(status.Notes, fmt.Sprintf("Normalization reported %d coverage warning(s).", status.LatestSnapshot.WarningCount))
		}
	}
	job, ok, err := service.Store.SupplyChainActiveJob(ctx, repo.ID, streamKey)
	if err != nil {
		return api.SupplyChainRepositoryStatus{}, err
	}
	if ok {
		summary := jobSummary(job, repo.GitHubID)
		status.ActiveJob = &summary
	}
	return status, nil
}

// componentCursor pages by document ordinal within one snapshot so a page is
// stable while the snapshot (immutable) exists.
type componentCursor struct {
	Version    int    `json:"v"`
	SnapshotID int64  `json:"s"`
	Ordinal    int    `json:"o"`
	Search     string `json:"q,omitempty"`
}

func DecodeComponentCursor(value string) (componentCursor, bool) {
	if value == "" {
		return componentCursor{}, true
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) > 512 {
		return componentCursor{}, false
	}
	var cursor componentCursor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.SnapshotID < 1 || cursor.Ordinal < 0 {
		return componentCursor{}, false
	}
	return cursor, true
}

func encodeComponentCursor(cursor componentCursor) string {
	cursor.Version = 1
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

// Components pages the latest snapshot's occurrences for the selected stream
// (or a specific authorized snapshot). Scope is computed from the snapshot's
// resolved DEPENDS_ON edges leaving a root; without roots every scope is
// unknown rather than presented as direct.
func (service *Service) Components(ctx context.Context, principal authn.Principal, githubID int64, streamKey string, snapshotID int64, cursor string, limit int, search string) (api.SupplyChainComponentList, error) {
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return api.SupplyChainComponentList{}, err
	}
	if len(search) > 200 {
		return api.SupplyChainComponentList{}, ErrInvalidRequest
	}
	repo, err := service.authorizedRepository(ctx, principal, githubID)
	if err != nil {
		return api.SupplyChainComponentList{}, err
	}
	page, ok := DecodeComponentCursor(cursor)
	if !ok {
		return api.SupplyChainComponentList{}, ErrInvalidRequest
	}
	if page.Version == 1 {
		if snapshotID != 0 && snapshotID != page.SnapshotID || page.Search != search {
			return api.SupplyChainComponentList{}, ErrInvalidRequest
		}
		snapshotID = page.SnapshotID
	}
	var snapshot Snapshot
	if snapshotID == 0 {
		stream, err := service.Store.SupplyChainStream(ctx, repo.ID, streamKey)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && stream.LatestSnapshotID == nil {
			return api.SupplyChainComponentList{}, ErrNoInventory
		}
		if err != nil {
			return api.SupplyChainComponentList{}, err
		}
		snapshotID = *stream.LatestSnapshotID
	}
	snapshot, err = service.Store.SupplyChainSnapshot(ctx, snapshotID, []int64{repo.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SupplyChainComponentList{}, ErrNotFound
	}
	if err != nil {
		return api.SupplyChainComponentList{}, err
	}
	if limit <= 0 || limit > service.maxResults() {
		limit = service.maxResults()
	}
	after := -1
	if page.Version == 1 {
		after = page.Ordinal
	}
	components, err := service.Store.SupplyChainComponents(ctx, snapshot.ID, after, limit+1, search)
	if err != nil {
		return api.SupplyChainComponentList{}, err
	}
	scopes, err := service.scopes(ctx, snapshot)
	if err != nil {
		return api.SupplyChainComponentList{}, err
	}
	result := api.SupplyChainComponentList{SnapshotID: snapshot.ID, Components: make([]api.SupplyChainComponent, 0, len(components))}
	for index, component := range components {
		if index == limit {
			result.Truncated = true
			result.NextCursor = encodeComponentCursor(componentCursor{SnapshotID: snapshot.ID, Ordinal: components[index-1].Ordinal, Search: search})
			break
		}
		result.Components = append(result.Components, componentSummary(component, scopes))
	}
	return result, nil
}

// scopes derives root/direct/transitive/unknown per element from resolved
// DEPENDS_ON edges. It is bounded by the snapshot's own edge count.
func (service *Service) scopes(ctx context.Context, snapshot Snapshot) (map[string]string, error) {
	scopes := map[string]string{}
	if len(snapshot.RootElementIDs) == 0 {
		return scopes, nil
	}
	edges, err := service.Store.SupplyChainRelationships(ctx, snapshot.ID, "", snapshot.EdgeCount+1)
	if err != nil {
		return nil, err
	}
	children := map[string][]string{}
	for _, edge := range edges {
		if edge.Type == "DEPENDS_ON" && edge.Resolved {
			children[edge.FromElement] = append(children[edge.FromElement], edge.ToElement)
		}
	}
	frontier := []string{}
	for _, root := range snapshot.RootElementIDs {
		scopes[root] = "root"
		frontier = append(frontier, root)
	}
	depth := 0
	for len(frontier) > 0 && depth < 64 {
		depth++
		var next []string
		for _, element := range frontier {
			for _, child := range children[element] {
				if _, seen := scopes[child]; seen {
					continue
				}
				if depth == 1 {
					scopes[child] = "direct"
				} else {
					scopes[child] = "transitive"
				}
				next = append(next, child)
			}
		}
		frontier = next
	}
	return scopes, nil
}

// Document returns the original bytes of an authorized snapshot's document.
// Authorization is evaluated at retrieval time with the live principal.
func (service *Service) Document(ctx context.Context, principal authn.Principal, snapshotID int64) ([]byte, string, string, Snapshot, error) {
	if snapshotID < 1 {
		return nil, "", "", Snapshot{}, ErrInvalidRequest
	}
	ids, err := service.AuthorizedRepositoryIDs(ctx, principal)
	if err != nil {
		return nil, "", "", Snapshot{}, err
	}
	snapshot, err := service.Store.SupplyChainSnapshot(ctx, snapshotID, ids)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", "", Snapshot{}, ErrNotFound
	}
	if err != nil {
		return nil, "", "", Snapshot{}, err
	}
	body, mediaType, digest, err := service.Store.SupplyChainDocument(ctx, snapshotID, ids)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", "", Snapshot{}, ErrNotFound
	}
	if err != nil {
		return nil, "", "", Snapshot{}, err
	}
	return body, mediaType, hex.EncodeToString(digest), snapshot, nil
}

func (service *Service) Snapshots(ctx context.Context, principal authn.Principal, githubID int64, streamKey string, limit int) ([]api.SupplyChainSnapshot, error) {
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return nil, err
	}
	repo, err := service.authorizedRepository(ctx, principal, githubID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > service.maxResults() {
		limit = service.maxResults()
	}
	snapshots, err := service.Store.SupplyChainSnapshots(ctx, repo.ID, streamKey, 0, limit)
	if err != nil {
		return nil, err
	}
	result := make([]api.SupplyChainSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		result = append(result, snapshotSummary(snapshot))
	}
	return result, nil
}

func (service *Service) Collections(ctx context.Context, principal authn.Principal, githubID int64, streamKey string, cursor string, limit int) (api.SupplyChainCollectionList, error) {
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return api.SupplyChainCollectionList{}, err
	}
	repo, err := service.authorizedRepository(ctx, principal, githubID)
	if err != nil {
		return api.SupplyChainCollectionList{}, err
	}
	var before int64
	if cursor != "" {
		before, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || before < 1 {
			return api.SupplyChainCollectionList{}, ErrInvalidRequest
		}
	}
	if limit <= 0 || limit > service.maxResults() {
		limit = service.maxResults()
	}
	collections, err := service.Store.SupplyChainCollections(ctx, repo.ID, streamKey, before, limit+1)
	if err != nil {
		return api.SupplyChainCollectionList{}, err
	}
	result := api.SupplyChainCollectionList{Collections: make([]api.SupplyChainCollection, 0, len(collections))}
	for index, collection := range collections {
		if index == limit {
			result.Truncated = true
			result.NextCursor = strconv.FormatInt(collections[index-1].ID, 10)
			break
		}
		result.Collections = append(result.Collections, collectionSummary(collection))
	}
	return result, nil
}

// Refresh enqueues bounded background collection. It never collects inline.
// Administrator access is the bootstrap write permission (decision D5).
func (service *Service) Refresh(ctx context.Context, principal authn.Principal, githubID int64, streamKey string) (api.SupplyChainRefreshResponse, error) {
	if !principal.Administrator {
		return api.SupplyChainRefreshResponse{}, ErrForbidden
	}
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return api.SupplyChainRefreshResponse{}, err
	}
	if streamKey != StreamGitHubSource {
		return api.SupplyChainRefreshResponse{}, ErrInvalidRequest
	}
	repo, err := service.authorizedRepository(ctx, principal, githubID)
	if err != nil {
		return api.SupplyChainRefreshResponse{}, err
	}
	job, created, err := service.Store.EnqueueSupplyChainJob(ctx, repo.ID, streamKey, "manual", principal.Subject, 10, service.now())
	if err != nil {
		return api.SupplyChainRefreshResponse{}, err
	}
	return api.SupplyChainRefreshResponse{Job: jobSummary(job, repo.GitHubID), Created: created}, nil
}

func (service *Service) JobStatus(ctx context.Context, principal authn.Principal, jobID int64) (api.SupplyChainJob, error) {
	if jobID < 1 {
		return api.SupplyChainJob{}, ErrInvalidRequest
	}
	repositories, err := service.Authorizer.AllAuthorizedRepositories(ctx, principal)
	if err != nil {
		return api.SupplyChainJob{}, err
	}
	ids := make([]int64, 0, len(repositories))
	githubIDs := map[int64]int64{}
	for _, repo := range repositories {
		ids = append(ids, repo.ID)
		githubIDs[repo.ID] = repo.GitHubID
	}
	job, err := service.Store.SupplyChainJob(ctx, jobID, ids)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SupplyChainJob{}, ErrNotFound
	}
	if err != nil {
		return api.SupplyChainJob{}, err
	}
	return jobSummary(job, githubIDs[job.RepositoryID]), nil
}

func snapshotSummary(snapshot Snapshot) api.SupplyChainSnapshot {
	warnings := make([]api.SupplyChainWarning, 0, len(snapshot.Warnings))
	for _, warning := range snapshot.Warnings {
		warnings = append(warnings, api.SupplyChainWarning{Code: warning.Code, Element: warning.Element, Detail: warning.Detail})
	}
	roots := snapshot.RootElementIDs
	if roots == nil {
		roots = []string{}
	}
	return api.SupplyChainSnapshot{
		ID: snapshot.ID, RepositoryID: snapshot.RepositoryID, Stream: snapshot.StreamKey, Producer: string(snapshot.Producer), Subject: string(snapshot.Subject),
		CollectedAt: snapshot.CollectedAt, CreatedAtClaimed: snapshot.CreatedAtClaimed, ProducerTool: snapshot.ProducerTool, DocumentName: snapshot.DocumentName,
		DocumentNS: snapshot.DocumentNamespace, SPDXVersion: snapshot.SPDXVersion, DataLicense: snapshot.DataLicense, SubjectRevision: snapshot.SubjectRevision,
		SubjectAssurance: string(snapshot.SubjectAssurance), RootElementIDs: roots, ParserVersion: snapshot.ParserVersion, ComponentCount: snapshot.ComponentCount,
		EdgeCount: snapshot.EdgeCount, WarningCount: snapshot.WarningCount, Warnings: warnings, PublishedAt: snapshot.PublishedAt,
		DocumentSHA256: hex.EncodeToString(snapshot.DocumentSHA256), DocumentFormat: string(snapshot.DocumentFormat), DocumentBytes: snapshot.DocumentBytes,
	}
}

func documentRef(snapshot Snapshot) api.SupplyChainDocumentRef {
	return api.SupplyChainDocumentRef{SnapshotID: snapshot.ID, SHA256: hex.EncodeToString(snapshot.DocumentSHA256), Format: string(snapshot.DocumentFormat), Bytes: snapshot.DocumentBytes,
		Path: "/v1/supply-chain/snapshots/" + strconv.FormatInt(snapshot.ID, 10) + "/document"}
}

func componentSummary(component Component, scopes map[string]string) api.SupplyChainComponent {
	scope := scopes[component.ElementID]
	if scope == "" {
		scope = "unknown"
	}
	checksums := make([]api.SupplyChainChecksum, 0, len(component.Checksums))
	for _, checksum := range component.Checksums {
		checksums = append(checksums, api.SupplyChainChecksum{Algorithm: checksum.Algorithm, Value: checksum.Value})
	}
	qualifiers := component.Qualifiers
	if len(qualifiers) == 0 {
		qualifiers = nil
	}
	return api.SupplyChainComponent{
		ElementID: component.ElementID, Ordinal: component.Ordinal, Name: component.Name, Version: component.Version, PURL: component.PURL, Ecosystem: component.Ecosystem,
		Namespace: component.PURLNamespace, PackageName: component.PURLName, Qualifiers: qualifiers, LicenseDeclaredRaw: component.LicenseDeclaredRaw,
		LicenseConcludedRaw: component.LicenseConcludedRaw, DownloadLocation: component.DownloadLocation, Supplier: component.Supplier, Checksums: checksums,
		IsRoot: component.IsRoot, Scope: scope,
	}
}

func collectionSummary(collection Collection) api.SupplyChainCollection {
	return api.SupplyChainCollection{
		ID: collection.ID, JobID: collection.JobID, Producer: string(collection.Producer), Stream: collection.StreamKey, StartedAt: collection.StartedAt, FinishedAt: collection.FinishedAt,
		Outcome: string(collection.Outcome), HTTPStatus: collection.HTTPStatus, RetryAfterSeconds: collection.RetryAfterSeconds, SnapshotID: collection.SnapshotID,
		ErrorCode: collection.ErrorCode, Message: collection.Message, ProjectionError: collection.ProjectionError,
	}
}

func jobSummary(job Job, githubID int64) api.SupplyChainJob {
	return api.SupplyChainJob{
		ID: job.ID, RepositoryID: githubID, Stream: job.StreamKey, Reason: job.Reason, State: string(job.State), Attempt: job.Attempt, MaxAttempts: job.MaxAttempts,
		RunAfter: job.RunAfter, ErrorCode: job.ErrorCode, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt, LeaseExpires: job.LeaseExpiresAt,
	}
}
