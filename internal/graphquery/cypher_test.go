package graphquery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/grepnest/grepnest/internal/graphprotocol"
)

func TestCypherRequiresAdminAndRejectsWrites(t *testing.T) {
	service := seededQueryService(t, callChain("A"))
	request := graphprotocol.CypherRequest{Statement: `CREATE (:File {uid: "bad"})`}
	if _, err := service.Cypher(t.Context(), request); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("Cypher() error = %v", err)
	}
	request.Admin = true
	if _, err := service.Cypher(t.Context(), request); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
}

func TestCypherBoundsRowsAndOutput(t *testing.T) {
	service := seededQueryService(t, callChain("A", "B", "C"))
	rows, err := service.Cypher(t.Context(), graphprotocol.CypherRequest{
		Admin: true, Statement: `UNWIND range(1, 5) AS value RETURN value`,
		MaxRows: 2, MaxBytes: 1 << 20, Parameters: map[string]any{"scalar": int64(1)},
	})
	if err != nil || len(rows.Rows) != 2 || !rows.Truncated {
		t.Fatalf("Cypher()=%#v,%v", rows, err)
	}
	bytes, err := service.Cypher(t.Context(), graphprotocol.CypherRequest{
		Admin: true, Statement: `RETURN $value`, Parameters: map[string]any{"value": strings.Repeat("x", 100)},
		MaxRows: 10, MaxBytes: 64,
	})
	if err != nil || !bytes.Truncated {
		t.Fatalf("Cypher()=%#v,%v", bytes, err)
	}
}

func TestCypherRejectsNonscalarParametersAndTimesOut(t *testing.T) {
	service := seededQueryService(t, callChain("A"))
	if _, err := service.Cypher(t.Context(), graphprotocol.CypherRequest{
		Admin: true, Statement: `RETURN $value`, Parameters: map[string]any{"value": []string{"bad"}},
	}); err == nil {
		t.Fatal("nonscalar parameter unexpectedly succeeded")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	_, err := service.Cypher(ctx, graphprotocol.CypherRequest{
		Admin: true, Statement: `UNWIND range(1, 1000000) AS value RETURN sum(value)`,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Cypher() error = %v, want deadline exceeded", err)
	}
}

func TestCypherStripsPhysicalUIDs(t *testing.T) {
	service := seededQueryService(t, callChain("A"))
	got, err := service.Cypher(t.Context(), graphprotocol.CypherRequest{
		Admin: true, Statement: `MATCH (s:Symbol) RETURN s.uid AS value LIMIT 1`,
	})
	if err != nil || len(got.Rows) != 1 || got.Rows[0][0] != "A" {
		t.Fatalf("Cypher()=%#v,%v", got, err)
	}
}

func TestCypherSanitizerPreservesOrdinaryNumericPrefixStrings(t *testing.T) {
	value := []any{
		"2026: forecast",
		map[string]any{"uid": "101:A", "note": "2026: forecast"},
		lbug.Node{Label: "Symbol", Properties: map[string]any{
			"repository_id": int64(101), "uid": "101:A", "note": "2026: forecast",
		}},
	}
	got := sanitizeCypherValue(value, "", nil)
	list := got.([]any)
	if list[0] != "2026: forecast" {
		t.Fatalf("ordinary string = %#v", list[0])
	}
	mapped := list[1].(map[string]any)
	if mapped["uid"] != "A" || mapped["note"] != "2026: forecast" {
		t.Fatalf("map = %#v", mapped)
	}
	node := list[2].(lbug.Node)
	if node.Properties["uid"] != "A" || node.Properties["note"] != "2026: forecast" {
		t.Fatalf("node = %#v", node)
	}
}

func TestCypherRejectsNegativeBounds(t *testing.T) {
	service := seededQueryService(t, callChain("A"))
	for name, request := range map[string]graphprotocol.CypherRequest{
		"rows":  {Admin: true, Statement: "RETURN 1", MaxRows: -1},
		"bytes": {Admin: true, Statement: "RETURN 1", MaxBytes: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Cypher(t.Context(), request); err == nil {
				t.Fatal("negative bound unexpectedly succeeded")
			}
		})
	}
}
