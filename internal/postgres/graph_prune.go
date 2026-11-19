package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// GraphPruned counts what one PruneRetiredGraphGenerations call deleted.
type GraphPruned struct {
	// Generations is 1 when the call deleted the last facts of a generation
	// and its graph_uploads row.
	Generations int64
	// Rows counts deleted fact rows, not the graph_uploads row.
	Rows int64
}

// graphPruneStatements delete a batch of one generation's facts, dependents
// first: the v2 edge, unresolved and discovery rows reference nodes, and the
// v1 edges cascade from their nodes, which would make a node batch unbounded.
// array() runs the limited subquery once, so a batch never exceeds its limit.
var graphPruneStatements = []string{
	`delete from graph_edges where id=any(array(select id from graph_edges where upload_id=$1 limit $2))`,
	`delete from graph_nodes where id=any(array(select id from graph_nodes where upload_id=$1 limit $2))`,
	`delete from graph_v2_discovery where upload_id=$1 and occurrence_key=any(array(select occurrence_key from graph_v2_discovery where upload_id=$1 limit $2))`,
	`delete from graph_v2_edges where upload_id=$1 and occurrence_key=any(array(select occurrence_key from graph_v2_edges where upload_id=$1 limit $2))`,
	`delete from graph_v2_unresolved where upload_id=$1 and occurrence_key=any(array(select occurrence_key from graph_v2_unresolved where upload_id=$1 limit $2))`,
	`delete from graph_v2_diagnostics where upload_id=$1 and occurrence_key=any(array(select occurrence_key from graph_v2_diagnostics where upload_id=$1 limit $2))`,
	`delete from graph_v2_files where upload_id=$1 and path_key=any(array(select path_key from graph_v2_files where upload_id=$1 limit $2))`,
	`delete from graph_v2_nodes where upload_id=$1 and occurrence_key=any(array(select occurrence_key from graph_v2_nodes where upload_id=$1 limit $2))`,
}

// PruneRetiredGraphGenerations deletes at most limit fact rows of the
// generation that has been retired longest, in one transaction, and the
// generation itself once no facts are left. Only generations retired for longer
// than grace are touched: a request that pinned a generation just before its
// replacement committed still reads it and answers generation_changed. A zero
// result means nothing is due. Concurrent callers take different generations.
func (s *Store) PruneRetiredGraphGenerations(ctx context.Context, grace time.Duration, limit int) (GraphPruned, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return GraphPruned{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `select id from graph_uploads where not active and retired_at < now() - $1::interval
		order by retired_at, id limit 1 for update skip locked`, grace).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return GraphPruned{}, nil
	}
	if err != nil {
		return GraphPruned{}, err
	}
	var pruned GraphPruned
	for _, statement := range graphPruneStatements {
		result, err := tx.Exec(ctx, statement, id, int64(limit)-pruned.Rows)
		if err != nil {
			return GraphPruned{}, err
		}
		if pruned.Rows += result.RowsAffected(); pruned.Rows >= int64(limit) {
			return pruned, tx.Commit(ctx)
		}
	}
	if _, err := tx.Exec(ctx, `delete from graph_uploads where id=$1`, id); err != nil {
		return GraphPruned{}, err
	}
	pruned.Generations = 1
	return pruned, tx.Commit(ctx)
}
