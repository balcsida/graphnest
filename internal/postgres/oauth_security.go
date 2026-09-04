package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
	"time"
)

const oauthRequestLockNamespace int32 = 0x676e6f61

// AllowOAuthRequest shares fixed-minute budgets across every server instance.
// Only the socket peer's canonical IP contributes to the source budget.
func (s *Store) AllowOAuthRequest(ctx context.Context, remoteAddr, endpoint string, now time.Time) (bool, error) {
	var sourceLimit, globalLimit int
	switch endpoint {
	case "/oauth/register":
		sourceLimit, globalLimit = 10, 100
	case "/oauth/token", "/oauth/revoke":
		sourceLimit, globalLimit = 60, 1000
	default:
		return false, errors.New("oauth: unsupported rate-limited endpoint")
	}
	peer, err := netip.ParseAddrPort(remoteAddr)
	if err != nil {
		return false, errors.New("oauth: invalid request peer")
	}
	source := sha256.Sum256([]byte(peer.Addr().Unmap().WithZone("").String()))
	window := now.UTC().Truncate(time.Minute)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// ponytail: serialize each endpoint; replace with row locks if budgeted throughput outgrows this lock.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1,hashtext($2))`, oauthRequestLockNamespace, endpoint); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `delete from oauth_request_limits where endpoint=$1 and window_start < $2`, endpoint, window); err != nil {
		return false, err
	}
	// Charge the deployment budget first so unbounded source addresses cannot
	// create unbounded database rows, even when individual sources are denied.
	for _, budget := range []struct {
		source []byte
		limit  int
	}{{[]byte{}, globalLimit}, {source[:], sourceLimit}} {
		result, err := tx.Exec(ctx, `insert into oauth_request_limits(endpoint,source_hash,window_start,request_count)
			values($1,$2,$3,1) on conflict(endpoint,source_hash) do update
			set request_count=oauth_request_limits.request_count+1
			where oauth_request_limits.window_start=$3 and oauth_request_limits.request_count < $4`, endpoint, budget.source, window, budget.limit)
		if err != nil {
			return false, err
		}
		if result.RowsAffected() == 0 {
			return false, tx.Commit(ctx)
		}
	}
	return true, tx.Commit(ctx)
}
