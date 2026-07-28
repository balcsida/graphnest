package graphservice

import (
	"context"
	"encoding/json"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/pkg/api"
)

func (s *Service) Cypher(ctx context.Context, principal authn.Principal, request api.GraphCypherRequest) (api.GraphCypherResponse, error) {
	if !principal.Administrator {
		return api.GraphCypherResponse{}, ErrAdminRequired
	}
	if request.Statement == "" || request.MaxRows < 0 || request.MaxBytes < 0 {
		return api.GraphCypherResponse{}, ErrInvalidRequest
	}
	selected, scope, _, err := s.scope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return api.GraphCypherResponse{}, err
	}
	parameters := make(map[string]any, len(request.Parameters))
	for name, raw := range request.Parameters {
		var value any
		if name == "" || json.Unmarshal(raw, &value) != nil {
			return api.GraphCypherResponse{}, ErrInvalidRequest
		}
		parameters[name] = value
	}
	response, err := s.Backend.Cypher(ctx, graphprotocol.CypherRequest{Scope: scope, Admin: true, Statement: request.Statement, Parameters: parameters, MaxRows: request.MaxRows, MaxBytes: request.MaxBytes})
	if err != nil {
		return api.GraphCypherResponse{}, err
	}
	if err := s.reauthorize(ctx, principal, selected, response.Commits); err != nil {
		return api.GraphCypherResponse{}, err
	}
	result := api.GraphCypherResponse{Status: "ok", Columns: response.Columns, Truncated: response.Truncated, Boundaries: boundaries(response.Boundaries), Commits: response.Commits}
	for _, row := range response.Rows {
		encoded := make([]json.RawMessage, len(row))
		for i, value := range row {
			bytes, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return api.GraphCypherResponse{}, marshalErr
			}
			encoded[i] = bytes
		}
		result.Rows = append(result.Rows, encoded)
	}
	return result, nil
}
