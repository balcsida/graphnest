package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultGraphQueryTimeout = 5 * time.Second

type Store struct {
	pool              *pgxpool.Pool
	graphQueryTimeout time.Duration
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, graphQueryTimeout: defaultGraphQueryTimeout}
}

func (s *Store) graphQueryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.graphQueryTimeout)
}
