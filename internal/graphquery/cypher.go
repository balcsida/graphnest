package graphquery

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/ladybug"
)

func (service *Service) Cypher(ctx context.Context, request graphprotocol.CypherRequest) (graphprotocol.CypherResponse, error) {
	if !request.Admin {
		return graphprotocol.CypherResponse{}, ErrAdminRequired
	}
	if strings.TrimSpace(request.Statement) == "" {
		return graphprotocol.CypherResponse{}, ErrInvalidRequest
	}
	if request.MaxRows < 0 || request.MaxBytes < 0 {
		return graphprotocol.CypherResponse{}, ErrInvalidRequest
	}
	for name, value := range request.Parameters {
		if name == "" || !scalar(value) {
			return graphprotocol.CypherResponse{}, ErrInvalidRequest
		}
	}
	response := graphprotocol.CypherResponse{}
	if len(request.Scope.Repositories) > 0 {
		ready, err := service.ready(ctx, request.Scope)
		if err != nil {
			return response, err
		}
		response.Boundaries, response.Commits = ready.boundaries, ready.commits
	}
	err := service.Database.View(ctx, func(session *ladybug.Session) error {
		repositoryIDs, err := cypherRepositoryIDs(ctx, session)
		if err != nil {
			return err
		}
		result, err := session.Execute(ctx, request.Statement, request.Parameters, ladybug.QueryLimits{
			MaxRows: request.MaxRows, MaxBytes: request.MaxBytes,
		})
		if err == nil {
			response.Columns, response.Rows, response.Truncated = result.Columns, result.Rows, result.Truncated
			for _, row := range response.Rows {
				for column := range row {
					row[column] = sanitizeCypherValue(row[column], response.Columns[column], repositoryIDs)
				}
			}
		}
		return err
	})
	return response, err
}

func cypherRepositoryIDs(ctx context.Context, session *ladybug.Session) (map[int64]struct{}, error) {
	const pageSize = 1_000
	ids := map[int64]struct{}{}
	var after int64
	for {
		result, err := session.Execute(ctx, `MATCH (r:Repository) WHERE r.id > $after RETURN r.id ORDER BY r.id LIMIT $limit`, map[string]any{
			"after": after, "limit": int64(pageSize),
		}, ladybug.QueryLimits{MaxRows: pageSize})
		if err != nil {
			return nil, err
		}
		for _, row := range result.Rows {
			after = row[0].(int64)
			ids[after] = struct{}{}
		}
		if result.Truncated && len(result.Rows) == 0 {
			return nil, errors.New("ladybug repository UID scope made no progress")
		}
		if !result.Truncated && len(result.Rows) < pageSize {
			return ids, nil
		}
	}
}

func scalar(value any) bool {
	switch value.(type) {
	case nil, bool, string,
		int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64,
		float32, float64, time.Time:
		return true
	default:
		return false
	}
}

func sanitizeCypherValue(value any, evidence string, repositoryIDs map[int64]struct{}) any {
	switch typed := value.(type) {
	case lbug.Node:
		if typed.Label == "File" || typed.Label == "Symbol" {
			repositoryID, _ := typed.Properties["repository_id"].(int64)
			if uid, ok := typed.Properties["uid"].(string); ok {
				typed.Properties["uid"] = stripStorageString(uid, repositoryID, repositoryIDs)
			}
		}
		typed.Properties = sanitizeProperties(typed.Properties, repositoryIDs)
		return typed
	case lbug.RecursiveRelationship:
		for index := range typed.Nodes {
			typed.Nodes[index] = sanitizeCypherValue(typed.Nodes[index], "", repositoryIDs).(lbug.Node)
		}
		return typed
	case []lbug.MapItem:
		for index := range typed {
			key, _ := typed[index].Key.(string)
			typed[index].Value = sanitizeCypherValue(typed[index].Value, key, repositoryIDs)
		}
		return typed
	case []any:
		for index := range typed {
			typed[index] = sanitizeCypherValue(typed[index], evidence, repositoryIDs)
		}
		return typed
	case map[string]any:
		return sanitizeProperties(typed, repositoryIDs)
	case string:
		if uidEvidence(evidence) || hasStoragePrefix(typed, repositoryIDs) {
			return stripStorageString(typed, 0, repositoryIDs)
		}
		return value
	default:
		return value
	}
}

func hasStoragePrefix(value string, repositoryIDs map[int64]struct{}) bool {
	if len(repositoryIDs) == 0 {
		return false
	}
	prefix, uid, found := strings.Cut(value, ":")
	id, err := strconv.ParseInt(prefix, 10, 64)
	if !found || err != nil || uid == "" || strings.TrimSpace(uid) != uid {
		return false
	}
	_, ok := repositoryIDs[id]
	return ok
}

func sanitizeProperties(properties map[string]any, repositoryIDs map[int64]struct{}) map[string]any {
	for key, value := range properties {
		if key == "uid" {
			properties[key] = sanitizeCypherValue(value, key, repositoryIDs)
		}
	}
	return properties
}

func uidEvidence(column string) bool {
	lower := strings.ToLower(column)
	return lower == "uid" || strings.Contains(lower, ".uid") || strings.Contains(lower, "uid)")
}

func stripStorageString(value string, repositoryID int64, repositoryIDs map[int64]struct{}) string {
	prefix, uid, found := strings.Cut(value, ":")
	id, err := strconv.ParseInt(prefix, 10, 64)
	if !found || err != nil || uid == "" || strings.TrimSpace(uid) != uid || repositoryID > 0 && id != repositoryID {
		return value
	}
	if len(repositoryIDs) > 0 {
		if _, ok := repositoryIDs[id]; !ok {
			return value
		}
	}
	return uid
}
