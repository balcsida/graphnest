package graphquery

import (
	"context"
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
		result, err := session.Execute(ctx, request.Statement, request.Parameters, ladybug.QueryLimits{
			MaxRows: request.MaxRows, MaxBytes: request.MaxBytes,
		})
		if err == nil {
			response.Columns, response.Rows, response.Truncated = result.Columns, result.Rows, result.Truncated
			for _, row := range response.Rows {
				for column := range row {
					row[column] = stripPhysicalUID(row[column])
				}
			}
		}
		return err
	})
	return response, err
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

func stripPhysicalUID(value any) any {
	switch typed := value.(type) {
	case lbug.Node:
		typed.Properties = stripProperties(typed.Properties)
		return typed
	case lbug.RecursiveRelationship:
		for index := range typed.Nodes {
			typed.Nodes[index].Properties = stripProperties(typed.Nodes[index].Properties)
		}
		return typed
	case []any:
		for index := range typed {
			typed[index] = stripPhysicalUID(typed[index])
		}
		return typed
	case map[string]any:
		return stripProperties(typed)
	case string:
		prefix, uid, found := strings.Cut(typed, ":")
		if _, err := strconv.ParseInt(prefix, 10, 64); found && err == nil {
			return uid
		}
		return value
	default:
		return value
	}
}

func stripProperties(properties map[string]any) map[string]any {
	for key, value := range properties {
		properties[key] = stripPhysicalUID(value)
	}
	return properties
}
