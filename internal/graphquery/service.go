package graphquery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/ladybug"
)

var (
	ErrInvalidRequest = errors.New("invalid graph query")
	ErrAdminRequired  = errors.New("administrator required")
)

const (
	defaultCategoryLimit = 100
	defaultImpactDepth   = 3
	defaultTraceDepth    = 10
	defaultMaxDepth      = 32
	defaultMaxTraceDepth = 30
	defaultMaxNodes      = 1_000
	defaultMaxEdges      = 5_000
	defaultMaxFanout     = 100
)

type Limits struct {
	PerCategory, MaxDepth, MaxTraceDepth, MaxNodes, MaxEdges, MaxFanout int
}

type Service struct {
	Database *ladybug.Database
	Limits   Limits
}

type readyScope struct {
	snapshots  []graphprotocol.RepositorySnapshot
	parameters []map[string]any
	boundaries []graphprotocol.Boundary
	commits    map[string]string
}

func (service *Service) ready(ctx context.Context, scope graphprotocol.Scope) (readyScope, error) {
	if service == nil || service.Database == nil || len(scope.Repositories) == 0 {
		return readyScope{}, ErrInvalidRequest
	}
	manifests, err := service.Database.Manifests(ctx)
	if err != nil {
		return readyScope{}, err
	}
	ready := readyScope{commits: map[string]string{}}
	seen := map[int64]struct{}{}
	for _, snapshot := range scope.Repositories {
		if snapshot.ID <= 0 || snapshot.Commit == "" {
			return readyScope{}, ErrInvalidRequest
		}
		if _, duplicate := seen[snapshot.ID]; duplicate {
			return readyScope{}, ErrInvalidRequest
		}
		seen[snapshot.ID] = struct{}{}
		manifest, ok := manifests[snapshot.ID]
		if !ok || manifest.Commit != snapshot.Commit {
			reason := "graph_not_ready"
			if !ok {
				reason = "graph_missing"
			}
			ready.boundaries = append(ready.boundaries, graphprotocol.Boundary{
				RepositoryID: snapshot.ID, Repository: snapshot.Name, Reason: reason,
			})
			continue
		}
		ready.snapshots = append(ready.snapshots, snapshot)
		ready.parameters = append(ready.parameters, map[string]any{"id": snapshot.ID, "commit": snapshot.Commit})
		ready.commits[snapshot.Name] = snapshot.Commit
	}
	return ready, nil
}

func (service *Service) limits() Limits {
	limits := service.Limits
	if limits.PerCategory <= 0 || limits.PerCategory > defaultCategoryLimit {
		limits.PerCategory = defaultCategoryLimit
	}
	if limits.MaxDepth <= 0 || limits.MaxDepth > defaultMaxDepth {
		limits.MaxDepth = defaultMaxDepth
	}
	if limits.MaxTraceDepth <= 0 || limits.MaxTraceDepth > defaultMaxTraceDepth {
		limits.MaxTraceDepth = defaultMaxTraceDepth
	}
	if limits.MaxNodes <= 0 || limits.MaxNodes > defaultMaxNodes {
		limits.MaxNodes = defaultMaxNodes
	}
	if limits.MaxEdges <= 0 || limits.MaxEdges > defaultMaxEdges {
		limits.MaxEdges = defaultMaxEdges
	}
	if limits.MaxFanout <= 0 || limits.MaxFanout > defaultMaxFanout {
		limits.MaxFanout = defaultMaxFanout
	}
	return limits
}

func symbolFromRow(row []any) graphprotocol.Symbol {
	repositoryID := row[0].(int64)
	path := row[3].(string)
	return graphprotocol.Symbol{
		RepositoryID: repositoryID,
		UID:          stripStorageUID(repositoryID, row[1].(string)),
		Name:         row[2].(string),
		FilePath:     path,
		Language:     row[4].(string),
		Kind:         row[5].(string),
		Signature:    row[6].(string),
		Range: graphprotocol.Position{
			StartLine: row[7].(int32), StartCharacter: row[8].(int32),
			EndLine: row[9].(int32), EndCharacter: row[10].(int32),
		},
		Test: isTestPath(path),
	}
}

func stripStorageUID(repositoryID int64, uid string) string {
	return strings.TrimPrefix(uid, strconv.FormatInt(repositoryID, 10)+":")
}

func qualifyUIDs(snapshots []graphprotocol.RepositorySnapshot, uids []string) []string {
	qualified := make([]string, 0, len(snapshots)*len(uids))
	for _, snapshot := range snapshots {
		for _, uid := range uids {
			qualified = append(qualified, fmt.Sprintf("%d:%s", snapshot.ID, uid))
		}
	}
	return qualified
}

func isTestPath(path string) bool {
	return strings.Contains(path, "/test/") || strings.Contains(path, "/tests/") ||
		strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".test.ts") ||
		strings.HasSuffix(path, ".test.js")
}

var relationQueries = map[string]struct{ incoming, outgoing string }{
	"calls": {
		incoming: `UNWIND $scope AS scope MATCH (r:Repository), (a:Symbol)-[:CALLS]->(b:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND a.repository_id = r.id AND b.uid IN $frontier AND $depth > 0 AND $min_confidence <= 1.0 RETURN a.repository_id, a.uid, a.qualified_name, a.path, a.language, a.kind, a.signature, a.start_line, a.start_character, a.end_line, a.end_character, b.uid ORDER BY a.uid SKIP $offset LIMIT $limit`,
		outgoing: `UNWIND $scope AS scope MATCH (r:Repository), (a:Symbol)-[:CALLS]->(b:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND b.repository_id = r.id AND a.uid IN $frontier AND $depth > 0 AND $min_confidence <= 1.0 RETURN b.repository_id, b.uid, b.qualified_name, b.path, b.language, b.kind, b.signature, b.start_line, b.start_character, b.end_line, b.end_character, a.uid ORDER BY b.uid SKIP $offset LIMIT $limit`,
	},
	"references": {
		incoming: `UNWIND $scope AS scope MATCH (r:Repository), (a:Symbol)-[:REFERENCES]->(b:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND a.repository_id = r.id AND b.uid IN $frontier AND $depth > 0 AND $min_confidence <= 1.0 RETURN a.repository_id, a.uid, a.qualified_name, a.path, a.language, a.kind, a.signature, a.start_line, a.start_character, a.end_line, a.end_character, b.uid ORDER BY a.uid SKIP $offset LIMIT $limit`,
		outgoing: `UNWIND $scope AS scope MATCH (r:Repository), (a:Symbol)-[:REFERENCES]->(b:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND b.repository_id = r.id AND a.uid IN $frontier AND $depth > 0 AND $min_confidence <= 1.0 RETURN b.repository_id, b.uid, b.qualified_name, b.path, b.language, b.kind, b.signature, b.start_line, b.start_character, b.end_line, b.end_character, a.uid ORDER BY b.uid SKIP $offset LIMIT $limit`,
	},
	"extends": {
		incoming: `UNWIND $scope AS scope MATCH (r:Repository), (a:Symbol)-[:EXTENDS]->(b:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND a.repository_id = r.id AND b.uid IN $frontier AND $depth > 0 AND $min_confidence <= 1.0 RETURN a.repository_id, a.uid, a.qualified_name, a.path, a.language, a.kind, a.signature, a.start_line, a.start_character, a.end_line, a.end_character, b.uid ORDER BY a.uid SKIP $offset LIMIT $limit`,
		outgoing: `UNWIND $scope AS scope MATCH (r:Repository), (a:Symbol)-[:EXTENDS]->(b:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND b.repository_id = r.id AND a.uid IN $frontier AND $depth > 0 AND $min_confidence <= 1.0 RETURN b.repository_id, b.uid, b.qualified_name, b.path, b.language, b.kind, b.signature, b.start_line, b.start_character, b.end_line, b.end_character, a.uid ORDER BY b.uid SKIP $offset LIMIT $limit`,
	},
	"implements": {
		incoming: `UNWIND $scope AS scope MATCH (r:Repository), (a:Symbol)-[:IMPLEMENTS]->(b:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND a.repository_id = r.id AND b.uid IN $frontier AND $depth > 0 AND $min_confidence <= 1.0 RETURN a.repository_id, a.uid, a.qualified_name, a.path, a.language, a.kind, a.signature, a.start_line, a.start_character, a.end_line, a.end_character, b.uid ORDER BY a.uid SKIP $offset LIMIT $limit`,
		outgoing: `UNWIND $scope AS scope MATCH (r:Repository), (a:Symbol)-[:IMPLEMENTS]->(b:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND b.repository_id = r.id AND a.uid IN $frontier AND $depth > 0 AND $min_confidence <= 1.0 RETURN b.repository_id, b.uid, b.qualified_name, b.path, b.language, b.kind, b.signature, b.start_line, b.start_character, b.end_line, b.end_character, a.uid ORDER BY b.uid SKIP $offset LIMIT $limit`,
	},
}

func selectedRelations(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return []string{"calls", "references", "extends", "implements"}, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(requested))
	for _, relation := range requested {
		if _, ok := relationQueries[relation]; !ok {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := seen[relation]; duplicate {
			continue
		}
		seen[relation] = struct{}{}
		out = append(out, relation)
	}
	return out, nil
}

func appendBoundary(boundaries []graphprotocol.Boundary, reason string, depth int) []graphprotocol.Boundary {
	return append(boundaries, graphprotocol.Boundary{Reason: reason, Depth: depth})
}
