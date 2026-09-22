package supplychain

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

// PortfolioStore is the cross-repository read side. Every method takes the
// caller's authorized repository IDs and never reads beyond them.
type PortfolioStore interface {
	SupplyChainOverview(context.Context, []int64, string, time.Time) (PortfolioOverview, error)
	SupplyChainPortfolioComponents(context.Context, PortfolioFilter) ([]PortfolioComponent, error)
	SupplyChainPortfolioFacets(context.Context, []int64, string) (map[string][]FacetCount, error)
	SupplyChainCoordinateOccurrences(context.Context, []int64, string, string, string, string, string, int) ([]PortfolioOccurrence, error)
	SnapshotComponentKeys(context.Context, int64) (map[string][2]string, error)
	SnapshotEdgeKeys(context.Context, int64) (map[string]bool, error)
	SupplyChainExportRows(context.Context, int64) ([]ExportRow, error)
}

// Portfolio serves the cross-repository views. It shares the Service's
// authorizer so REST and MCP agree on scope.
type Portfolio struct {
	Store      PortfolioStore
	Snapshots  Store
	Authorizer Authorizer
	Interval   time.Duration
	MaxResults int
	Now        func() time.Time
}

func (portfolio *Portfolio) now() time.Time {
	if portfolio.Now != nil {
		return portfolio.Now().UTC()
	}
	return time.Now().UTC()
}

func (portfolio *Portfolio) maxResults() int {
	if portfolio.MaxResults <= 0 || portfolio.MaxResults > 500 {
		return 100
	}
	return portfolio.MaxResults
}

func (portfolio *Portfolio) interval() time.Duration {
	if portfolio.Interval <= 0 {
		return 24 * time.Hour
	}
	return portfolio.Interval
}

// scope resolves the authorized repositories, optionally intersected with a
// requested selection of GitHub IDs. Unknown or unauthorized requested IDs
// are silently dropped so the response reveals nothing about them.
func (portfolio *Portfolio) scope(ctx context.Context, principal authn.Principal, requested []int64) ([]int64, map[int64]repository.Repository, error) {
	repositories, err := portfolio.Authorizer.AllAuthorizedRepositories(ctx, principal)
	if err != nil {
		return nil, nil, err
	}
	byGitHubID := make(map[int64]repository.Repository, len(repositories))
	wanted := map[int64]bool{}
	for _, id := range requested {
		wanted[id] = true
	}
	ids := make([]int64, 0, len(repositories))
	for _, repo := range repositories {
		byGitHubID[repo.GitHubID] = repo
		if len(requested) == 0 || wanted[repo.GitHubID] {
			ids = append(ids, repo.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, byGitHubID, nil
}

// Overview reports inventory state across the authorized repositories with
// every denominator named.
func (portfolio *Portfolio) Overview(ctx context.Context, principal authn.Principal, streamKey string, requested []int64) (api.SupplyChainOverview, error) {
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return api.SupplyChainOverview{}, err
	}
	ids, _, err := portfolio.scope(ctx, principal, requested)
	if err != nil {
		return api.SupplyChainOverview{}, err
	}
	overview, err := portfolio.Store.SupplyChainOverview(ctx, ids, streamKey, portfolio.now().Add(-2*portfolio.interval()))
	if err != nil {
		return api.SupplyChainOverview{}, err
	}
	result := api.SupplyChainOverview{
		Stream: streamKey, GeneratedAt: portfolio.now(),
		Repositories: api.SupplyChainRepositoryCounts{Authorized: overview.Repositories, WithInventory: overview.WithInventory, NeverCollected: overview.NeverCollected, Stale: overview.Stale, FailedLastAttempt: overview.Failed, OptedOut: overview.OptedOut},
		Components:   api.SupplyChainComponentCounts{Occurrences: overview.Occurrences, UniqueCoordinates: overview.UniqueCoordinates, WithoutPURL: overview.WithoutPURL, WithoutVersion: overview.WithoutVersion, Unassessed: overview.UnassessedComponent, Assessments: overview.AssessmentCounts},
		WarningTotal: overview.WarningTotal, OldestCollectedAt: overview.OldestCollectedAt, NewestCollectedAt: overview.NewestCollectedAt, Ecosystems: []api.SupplyChainFacet{},
		Denominators: []string{
			"repositories.* count repositories the caller may read; with_inventory + never_collected = authorized.",
			"stale counts inventories whose latest observation is older than twice the configured interval (" + portfolio.interval().String() + ").",
			"failed_last_attempt counts repositories whose most recent collection did not succeed, whether or not an older inventory is retained.",
			"components.occurrences counts component occurrences in the latest snapshot of the selected stream per repository; unique_coordinates groups them by (ecosystem, namespace, name, version).",
			"assessments count occurrences by derived license assessment; they describe evidence, not compliance or approval.",
		},
	}
	if result.Components.Assessments == nil {
		result.Components.Assessments = map[string]int{}
	}
	for _, facet := range overview.Ecosystems {
		result.Ecosystems = append(result.Ecosystems, api.SupplyChainFacet{Value: facet.Value, Count: facet.Count})
	}
	return result, nil
}

type portfolioCursor struct {
	Version int    `json:"v"`
	Key     string `json:"k"`
	Filter  string `json:"f"`
}

func decodePortfolioCursor(value string) (portfolioCursor, bool) {
	if value == "" {
		return portfolioCursor{}, true
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) > 2048 {
		return portfolioCursor{}, false
	}
	var cursor portfolioCursor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.Key == "" {
		return portfolioCursor{}, false
	}
	return cursor, true
}

func encodePortfolioCursor(key, filter string) string {
	data, _ := json.Marshal(portfolioCursor{Version: 1, Key: key, Filter: filter})
	return base64.RawURLEncoding.EncodeToString(data)
}

// ComponentsRequest bounds a portfolio component query.
type ComponentsRequest struct {
	Stream           string
	RepositoryIDs    []int64
	Ecosystem        string
	Search           string
	License          string
	AssessmentStatus string
	Cursor           string
	Limit            int
}

func (request ComponentsRequest) filterKey() string {
	ids := make([]string, 0, len(request.RepositoryIDs))
	for _, id := range request.RepositoryIDs {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	sort.Strings(ids)
	return strings.Join([]string{request.Stream, strings.Join(ids, ","), request.Ecosystem, request.Search, request.License, request.AssessmentStatus}, "\x1f")
}

var validAssessmentStatuses = map[string]bool{"": true, "unknown": true, "declared": true, "resolved": true, "conflict": true, "unlicensed": true, "not_applicable": true, "pending": true, "unassessed": true}

// Components lists unique coordinates across the authorized latest snapshots,
// server-paginated with a cursor bound to the filter set.
func (portfolio *Portfolio) Components(ctx context.Context, principal authn.Principal, request ComponentsRequest) (api.SupplyChainPortfolioComponentList, error) {
	stream, err := normalizeStream(request.Stream)
	if err != nil {
		return api.SupplyChainPortfolioComponentList{}, err
	}
	request.Stream = stream
	if len(request.Search) > 200 || len(request.License) > 500 || len(request.Ecosystem) > 64 || !validAssessmentStatuses[request.AssessmentStatus] || len(request.RepositoryIDs) > 200 {
		return api.SupplyChainPortfolioComponentList{}, ErrInvalidRequest
	}
	cursor, ok := decodePortfolioCursor(request.Cursor)
	if !ok || cursor.Version == 1 && cursor.Filter != request.filterKey() {
		return api.SupplyChainPortfolioComponentList{}, ErrInvalidRequest
	}
	ids, byGitHubID, err := portfolio.scope(ctx, principal, request.RepositoryIDs)
	if err != nil {
		return api.SupplyChainPortfolioComponentList{}, err
	}
	limit := request.Limit
	if limit <= 0 || limit > portfolio.maxResults() {
		limit = portfolio.maxResults()
	}
	items, err := portfolio.Store.SupplyChainPortfolioComponents(ctx, PortfolioFilter{RepositoryIDs: ids, StreamKey: stream, Ecosystem: strings.ToLower(request.Ecosystem), Search: request.Search,
		LicenseExpression: request.License, AssessmentStatus: request.AssessmentStatus, AfterKey: cursor.Key, Limit: limit + 1})
	if err != nil {
		return api.SupplyChainPortfolioComponentList{}, err
	}
	result := api.SupplyChainPortfolioComponentList{Stream: stream, Components: make([]api.SupplyChainPortfolioComponent, 0, len(items)), RepositoriesInScope: len(ids)}
	for index, item := range items {
		if index == limit {
			result.Truncated = true
			result.NextCursor = encodePortfolioCursor(items[index-1].Key, request.filterKey())
			break
		}
		result.Components = append(result.Components, portfolioComponent(item, byGitHubID))
	}
	return result, nil
}

func portfolioComponent(item PortfolioComponent, byGitHubID map[int64]repository.Repository) api.SupplyChainPortfolioComponent {
	repositories := make([]api.SupplyChainRepositoryRef, 0, len(item.RepositoryGitHubIDs))
	for _, githubID := range item.RepositoryGitHubIDs {
		ref := api.SupplyChainRepositoryRef{ID: githubID}
		if repo, ok := byGitHubID[githubID]; ok {
			ref.Name = repo.Name
		}
		repositories = append(repositories, ref)
	}
	statuses := item.AssessmentStatuses
	sort.Strings(statuses)
	assessment := "unassessed"
	switch {
	case len(statuses) == 1:
		assessment = statuses[0]
	case len(statuses) > 1:
		assessment = "mixed"
	}
	return api.SupplyChainPortfolioComponent{
		Key: hex.EncodeToString([]byte(item.Key)), Ecosystem: item.Ecosystem, Namespace: item.Namespace, Name: item.Name, Version: item.Version, PURL: item.PURL,
		RepositoryCount: item.RepositoryCount, OccurrenceCount: item.OccurrenceCount, Assessment: assessment, AssessmentStatuses: statuses, Expression: item.Expression,
		DeclaredRaw: item.DeclaredRaw, NewestCollectedAt: item.NewestCollectedAt, OldestCollectedAt: item.OldestCollectedAt, Repositories: repositories,
	}
}

// Facets returns bounded filter facets within the authorized scope.
func (portfolio *Portfolio) Facets(ctx context.Context, principal authn.Principal, streamKey string, requested []int64) (api.SupplyChainFacets, error) {
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return api.SupplyChainFacets{}, err
	}
	ids, _, err := portfolio.scope(ctx, principal, requested)
	if err != nil {
		return api.SupplyChainFacets{}, err
	}
	facets, err := portfolio.Store.SupplyChainPortfolioFacets(ctx, ids, streamKey)
	if err != nil {
		return api.SupplyChainFacets{}, err
	}
	convert := func(items []FacetCount) []api.SupplyChainFacet {
		result := make([]api.SupplyChainFacet, 0, len(items))
		for _, item := range items {
			result = append(result, api.SupplyChainFacet{Value: item.Value, Count: item.Count})
		}
		return result
	}
	return api.SupplyChainFacets{Stream: streamKey, Ecosystems: convert(facets["ecosystem"]), Assessments: convert(facets["assessment"]), Licenses: convert(facets["license"])}, nil
}

// DecodeCoordinateKey turns a portfolio component key back into coordinates.
func DecodeCoordinateKey(key string) (ecosystem, namespace, name, version string, ok bool) {
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) > 2048 {
		return "", "", "", "", false
	}
	parts := strings.Split(string(raw), "\x01")
	if len(parts) != 4 || parts[2] == "" {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
}

// Component returns the cross-repository detail of one coordinate: where it
// occurs within the authorized set and with what assessment.
func (portfolio *Portfolio) Component(ctx context.Context, principal authn.Principal, streamKey, key string, requested []int64) (api.SupplyChainPortfolioComponentDetail, error) {
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return api.SupplyChainPortfolioComponentDetail{}, err
	}
	ecosystem, namespace, name, version, ok := DecodeCoordinateKey(key)
	if !ok {
		return api.SupplyChainPortfolioComponentDetail{}, ErrInvalidRequest
	}
	ids, _, err := portfolio.scope(ctx, principal, requested)
	if err != nil {
		return api.SupplyChainPortfolioComponentDetail{}, err
	}
	occurrences, err := portfolio.Store.SupplyChainCoordinateOccurrences(ctx, ids, streamKey, ecosystem, namespace, name, version, portfolio.maxResults()+1)
	if err != nil {
		return api.SupplyChainPortfolioComponentDetail{}, err
	}
	if len(occurrences) == 0 {
		return api.SupplyChainPortfolioComponentDetail{}, ErrNotFound
	}
	detail := api.SupplyChainPortfolioComponentDetail{Key: key, Stream: streamKey, Ecosystem: ecosystem, Namespace: namespace, Name: name, Version: version, Occurrences: []api.SupplyChainPortfolioOccurrence{},
		Notes: []string{"Occurrences are listed only for repositories the caller may read; counts elsewhere in the portfolio use the same scope.", "A dependency path is not a call graph, and an inventory occurrence does not prove the package is shipped or used."}}
	for index, occurrence := range occurrences {
		if index == portfolio.maxResults() {
			detail.Truncated = true
			break
		}
		detail.Occurrences = append(detail.Occurrences, api.SupplyChainPortfolioOccurrence{RepositoryID: occurrence.RepositoryGitHubID, Repository: occurrence.Repository, SnapshotID: occurrence.SnapshotID,
			CollectedAt: occurrence.CollectedAt, ElementID: occurrence.ElementID, Root: occurrence.Scope == "root", DeclaredRaw: occurrence.DeclaredRaw, Assessment: occurrence.AssessmentStatus, Expression: occurrence.Expression,
			DetailPath: "/v1/supply-chain/repositories/" + strconv.FormatInt(occurrence.RepositoryGitHubID, 10) + "/component?element=" + urlQueryEscape(occurrence.ElementID) + "&snapshot_id=" + strconv.FormatInt(occurrence.SnapshotID, 10)})
	}
	return detail, nil
}

func urlQueryEscape(value string) string {
	var builder strings.Builder
	for _, b := range []byte(value) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.' || b == '~' {
			builder.WriteByte(b)
			continue
		}
		builder.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{b})))
	}
	return builder.String()
}

// Compare diffs two authorized snapshots of the same repository: component
// coordinate changes, declared-license changes, resolved-edge changes, and
// metadata-only changes (timestamps, IDs, parser version) reported apart.
func (portfolio *Portfolio) Compare(ctx context.Context, principal authn.Principal, githubID, baseID, headID int64) (api.SupplyChainSnapshotComparison, error) {
	if githubID < 1 || baseID < 1 || headID < 1 {
		return api.SupplyChainSnapshotComparison{}, ErrInvalidRequest
	}
	repo, err := portfolio.Authorizer.AuthorizedRepository(ctx, principal, githubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SupplyChainSnapshotComparison{}, ErrNotFound
	}
	if err != nil {
		return api.SupplyChainSnapshotComparison{}, err
	}
	base, err := portfolio.Snapshots.SupplyChainSnapshot(ctx, baseID, []int64{repo.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SupplyChainSnapshotComparison{}, ErrNotFound
	}
	if err != nil {
		return api.SupplyChainSnapshotComparison{}, err
	}
	head, err := portfolio.Snapshots.SupplyChainSnapshot(ctx, headID, []int64{repo.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SupplyChainSnapshotComparison{}, ErrNotFound
	}
	if err != nil {
		return api.SupplyChainSnapshotComparison{}, err
	}
	baseKeys, err := portfolio.Store.SnapshotComponentKeys(ctx, base.ID)
	if err != nil {
		return api.SupplyChainSnapshotComparison{}, err
	}
	headKeys, err := portfolio.Store.SnapshotComponentKeys(ctx, head.ID)
	if err != nil {
		return api.SupplyChainSnapshotComparison{}, err
	}
	baseEdges, err := portfolio.Store.SnapshotEdgeKeys(ctx, base.ID)
	if err != nil {
		return api.SupplyChainSnapshotComparison{}, err
	}
	headEdges, err := portfolio.Store.SnapshotEdgeKeys(ctx, head.ID)
	if err != nil {
		return api.SupplyChainSnapshotComparison{}, err
	}
	comparison := api.SupplyChainSnapshotComparison{RepositoryID: repo.GitHubID, Base: snapshotSummary(base), Head: snapshotSummary(head), AddedComponents: []string{}, RemovedComponents: []string{},
		LicenseChanges: []api.SupplyChainLicenseChange{}, MetadataChanges: []string{}, Notes: []string{}}
	// Coordinates: by coordinate key, so a renamed SPDXID with the same package is not a change.
	baseByCoordinate := map[string][]string{}
	for element, pair := range baseKeys {
		baseByCoordinate[pair[0]] = append(baseByCoordinate[pair[0]], element)
	}
	headByCoordinate := map[string][]string{}
	for element, pair := range headKeys {
		headByCoordinate[pair[0]] = append(headByCoordinate[pair[0]], element)
	}
	for key := range headByCoordinate {
		if _, ok := baseByCoordinate[key]; !ok {
			comparison.AddedComponents = append(comparison.AddedComponents, displayCoordinate(key))
		}
	}
	for key := range baseByCoordinate {
		if _, ok := headByCoordinate[key]; !ok {
			comparison.RemovedComponents = append(comparison.RemovedComponents, displayCoordinate(key))
		}
	}
	sort.Strings(comparison.AddedComponents)
	sort.Strings(comparison.RemovedComponents)
	for element, headPair := range headKeys {
		if basePair, ok := baseKeys[element]; ok && basePair[0] == headPair[0] && basePair[1] != headPair[1] {
			comparison.LicenseChanges = append(comparison.LicenseChanges, api.SupplyChainLicenseChange{Component: displayCoordinate(headPair[0]), From: basePair[1], To: headPair[1]})
		}
	}
	sort.Slice(comparison.LicenseChanges, func(i, j int) bool {
		return comparison.LicenseChanges[i].Component < comparison.LicenseChanges[j].Component
	})
	for edge := range headEdges {
		if !baseEdges[edge] {
			comparison.EdgesAdded++
		}
	}
	for edge := range baseEdges {
		if !headEdges[edge] {
			comparison.EdgesRemoved++
		}
	}
	if base.ParserVersion != head.ParserVersion {
		comparison.MetadataChanges = append(comparison.MetadataChanges, "parser_version: "+strconv.Itoa(base.ParserVersion)+" → "+strconv.Itoa(head.ParserVersion))
	}
	if base.DocumentNamespace != head.DocumentNamespace {
		comparison.MetadataChanges = append(comparison.MetadataChanges, "document_namespace changed")
	}
	if (base.CreatedAtClaimed == nil) != (head.CreatedAtClaimed == nil) || base.CreatedAtClaimed != nil && head.CreatedAtClaimed != nil && !base.CreatedAtClaimed.Equal(*head.CreatedAtClaimed) {
		comparison.MetadataChanges = append(comparison.MetadataChanges, "producer-claimed creation time changed")
	}
	if base.ProducerTool != head.ProducerTool {
		comparison.MetadataChanges = append(comparison.MetadataChanges, "producer tool: "+base.ProducerTool+" → "+head.ProducerTool)
	}
	if base.Producer != head.Producer || base.Subject != head.Subject {
		comparison.Notes = append(comparison.Notes, "The snapshots come from different producers or subjects; their coverage differs and component differences may reflect scope, not change.")
	}
	if len(comparison.AddedComponents) == 0 && len(comparison.RemovedComponents) == 0 && len(comparison.LicenseChanges) == 0 && comparison.EdgesAdded == 0 && comparison.EdgesRemoved == 0 {
		comparison.Notes = append(comparison.Notes, "No component, license, or dependency-edge changes; any difference is document metadata only.")
	}
	return comparison, nil
}

func displayCoordinate(key string) string {
	parts := strings.Split(key, "\x01")
	if len(parts) != 4 {
		return key
	}
	name := parts[2]
	if parts[1] != "" {
		name = parts[1] + "/" + parts[2]
	}
	if parts[3] != "" {
		name += "@" + parts[3]
	}
	if parts[0] != "" {
		return parts[0] + ":" + name
	}
	return name
}

// ExportCSV writes the components of one authorized snapshot as CSV with
// snapshot and provenance identifiers in a header comment row. Cells that
// could be interpreted as spreadsheet formulas are prefixed with an
// apostrophe.
func (portfolio *Portfolio) ExportCSV(ctx context.Context, principal authn.Principal, githubID int64, streamKey string, snapshotID int64, writer io.Writer) (Snapshot, error) {
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return Snapshot{}, err
	}
	repo, err := portfolio.Authorizer.AuthorizedRepository(ctx, principal, githubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	if snapshotID == 0 {
		stream, err := portfolio.Snapshots.SupplyChainStream(ctx, repo.ID, streamKey)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && stream.LatestSnapshotID == nil {
			return Snapshot{}, ErrNoInventory
		}
		if err != nil {
			return Snapshot{}, err
		}
		snapshotID = *stream.LatestSnapshotID
	}
	snapshot, err := portfolio.Snapshots.SupplyChainSnapshot(ctx, snapshotID, []int64{repo.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	rows, err := portfolio.Store.SupplyChainExportRows(ctx, snapshot.ID)
	if err != nil {
		return Snapshot{}, err
	}
	out := csv.NewWriter(writer)
	header := []string{"repository", "snapshot_id", "stream", "producer", "collected_at", "document_sha256", "element_id", "name", "version", "purl", "ecosystem", "is_root", "license_declared_raw", "license_concluded_raw", "assessment_status", "assessed_expression"}
	if err := out.Write(header); err != nil {
		return Snapshot{}, err
	}
	for _, row := range rows {
		record := []string{repo.Name, strconv.FormatInt(snapshot.ID, 10), snapshot.StreamKey, string(snapshot.Producer), snapshot.CollectedAt.Format(time.RFC3339), hex.EncodeToString(snapshot.DocumentSHA256),
			row.ElementID, row.Name, row.Version, row.PURL, row.Ecosystem, strconv.FormatBool(row.IsRoot), row.DeclaredRaw, row.ConcludedRaw, row.AssessmentStatus, row.Expression}
		for index := range record {
			record[index] = csvSafe(record[index])
		}
		if err := out.Write(record); err != nil {
			return Snapshot{}, err
		}
	}
	out.Flush()
	return snapshot, out.Error()
}

// csvSafe neutralizes spreadsheet formula injection: values beginning with
// =, +, -, @, tab, or carriage return get a leading apostrophe.
func csvSafe(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r', '|', '%':
		return "'" + value
	}
	return value
}
