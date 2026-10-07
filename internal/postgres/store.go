package postgres

import (
	"context"
	"log/slog"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultGraphQueryTimeout = 5 * time.Second

type Store struct {
	pool              *pgxpool.Pool
	graphQueryTimeout time.Duration
	// Logger receives one record when a SCIP upload lands without its derived
	// v2 generation (see ReplaceSCIP); nil discards it.
	Logger *slog.Logger
	// scipGraphLimits bounds the generation derived from a SCIP upload; the
	// zero value uses the artifact defaults.
	scipGraphLimits graphartifact.Limits
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, graphQueryTimeout: defaultGraphQueryTimeout}
}

func (s *Store) graphQueryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.graphQueryTimeout)
}
