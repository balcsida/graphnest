package graphquery

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/grepnest/grepnest/internal/graphartifact"
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
	maxRows := request.MaxRows
	if maxRows <= 0 || maxRows > service.limits().MaxRows {
		maxRows = service.limits().MaxRows
	}
	ready, err := service.ready(ctx, request.Scope)
	if err != nil {
		return response, err
	}
	response.Boundaries, response.Commits = ready.boundaries, ready.commits
	if err := service.authorizeCypherScope(ctx, request.Scope); err != nil {
		return response, err
	}
	err = service.Database.View(ctx, func(session *ladybug.Session) error {
		result, err := session.Execute(ctx, request.Statement, request.Parameters, ladybug.QueryLimits{
			MaxRows: maxRows, MaxBytes: request.MaxBytes,
		})
		if err == nil {
			response.Columns, response.Rows, response.Truncated = result.Columns, result.Rows, result.Truncated
			candidates := map[string]struct{}{}
			for _, row := range response.Rows {
				for _, value := range row {
					collectUIDCandidates(value, candidates)
				}
			}
			physicalUIDs, findErr := existingPhysicalUIDs(ctx, session, candidates)
			if findErr != nil {
				return findErr
			}
			for _, row := range response.Rows {
				for column := range row {
					row[column] = sanitizeCypherValue(row[column], physicalUIDs)
				}
			}
		}
		return err
	})
	if err == nil {
		err = service.authorizeCypherScope(ctx, request.Scope)
	}
	return response, err
}

func (service *Service) authorizeCypherScope(ctx context.Context, scope graphprotocol.Scope) error {
	manifests, err := service.Database.Manifests(ctx)
	if err != nil {
		return err
	}
	allowed := make(map[int64]string, len(scope.Repositories))
	for _, snapshot := range scope.Repositories {
		allowed[snapshot.ID] = snapshot.Commit
	}
	for id, manifest := range manifests {
		if allowed[id] != manifest.Commit {
			return ErrUnauthorizedScope
		}
	}
	return nil
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

func collectUIDCandidates(value any, candidates map[string]struct{}) {
	switch typed := value.(type) {
	case lbug.Node:
		for _, property := range typed.Properties {
			collectUIDCandidates(property, candidates)
		}
	case lbug.RecursiveRelationship:
		for _, node := range typed.Nodes {
			collectUIDCandidates(node, candidates)
		}
	case []lbug.MapItem:
		for _, item := range typed {
			collectUIDCandidates(item.Value, candidates)
		}
	case []any:
		for _, item := range typed {
			collectUIDCandidates(item, candidates)
		}
	case map[string]any:
		for _, item := range typed {
			collectUIDCandidates(item, candidates)
		}
	case string:
		if storageUIDCandidate(typed) {
			candidates[typed] = struct{}{}
		}
	}
}

func existingPhysicalUIDs(ctx context.Context, session *ladybug.Session, candidates map[string]struct{}) (map[string]struct{}, error) {
	values := make([]string, 0, len(candidates))
	for candidate := range candidates {
		values = append(values, candidate)
	}
	sort.Strings(values)
	found := map[string]struct{}{}
	for start := 0; start < len(values); {
		end, bytes := start, 0
		for end < len(values) && end-start < 1_000 && bytes+len(values[end]) <= 64<<10 {
			bytes += len(values[end])
			end++
		}
		for _, query := range []string{
			`UNWIND $uids AS uid MATCH (n:File) WHERE n.uid = uid RETURN n.uid ORDER BY n.uid LIMIT $limit`,
			`UNWIND $uids AS uid MATCH (n:Symbol) WHERE n.uid = uid RETURN n.uid ORDER BY n.uid LIMIT $limit`,
		} {
			result, err := session.Execute(ctx, query, map[string]any{
				"uids": values[start:end], "limit": int64(end - start),
			}, ladybug.QueryLimits{MaxRows: end - start})
			if err != nil {
				return nil, err
			}
			if result.Truncated {
				return nil, errors.New("physical UID verification was truncated")
			}
			for _, row := range result.Rows {
				found[row[0].(string)] = struct{}{}
			}
		}
		start = end
	}
	return found, nil
}

func sanitizeCypherValue(value any, physicalUIDs map[string]struct{}) any {
	switch typed := value.(type) {
	case lbug.Node:
		typed.Properties = sanitizeProperties(typed.Properties, physicalUIDs)
		return typed
	case lbug.RecursiveRelationship:
		for index := range typed.Nodes {
			typed.Nodes[index] = sanitizeCypherValue(typed.Nodes[index], physicalUIDs).(lbug.Node)
		}
		return typed
	case []lbug.MapItem:
		for index := range typed {
			typed[index].Value = sanitizeCypherValue(typed[index].Value, physicalUIDs)
		}
		return typed
	case []any:
		for index := range typed {
			typed[index] = sanitizeCypherValue(typed[index], physicalUIDs)
		}
		return typed
	case map[string]any:
		return sanitizeProperties(typed, physicalUIDs)
	case string:
		if _, ok := physicalUIDs[typed]; ok {
			_, uid, _ := strings.Cut(typed, ":")
			return uid
		}
		return value
	default:
		return value
	}
}

func sanitizeProperties(properties map[string]any, physicalUIDs map[string]struct{}) map[string]any {
	for key, value := range properties {
		properties[key] = sanitizeCypherValue(value, physicalUIDs)
	}
	return properties
}

func storageUIDCandidate(value string) bool {
	if len(value) > graphartifact.DefaultMaxIdentifierBytes+20 {
		return false
	}
	prefix, uid, found := strings.Cut(value, ":")
	_, err := strconv.ParseInt(prefix, 10, 64)
	return found && err == nil && uid != ""
}
