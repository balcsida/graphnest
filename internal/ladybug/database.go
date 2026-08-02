package ladybug

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
)

const (
	defaultReadConnections = 8
	maxReadConnections     = 32
	defaultQueryTimeout    = 5 * time.Second
	defaultInterruptGrace  = 2 * time.Second
)

var (
	errClosed    = errors.New("ladybug database is closed")
	errUnhealthy = errors.New("ladybug database is unhealthy")
)

type Options struct {
	Path            string
	ReadConnections int
	QueryTimeout    time.Duration
	InterruptGrace  time.Duration
}

type Database struct {
	handle *lbug.Database
	// writers is a one-slot pool holding the single write connection. It is a
	// channel rather than a mutex so that ownership can be handed to a
	// reclaimer goroutine: a statement that outlives its interrupt grace leaves
	// the connection busy, and nothing may write again until it is clean.
	writers       chan *lbug.Connection
	readers       chan *lbug.Connection
	connections   []*lbug.Connection
	connectionsMu sync.Mutex
	options       Options
	operations    sync.RWMutex
	queries       sync.WaitGroup
	reclaims      sync.WaitGroup
	closeOnce     sync.Once
	closed        atomic.Bool
	unhealthy     atomic.Bool
}

func Open(options Options) (*Database, error) {
	if options.Path == "" {
		return nil, errors.New("ladybug database path is required")
	}
	options = normalizeOptions(options)
	handle, err := lbug.OpenDatabase(options.Path, lbug.DefaultSystemConfig())
	if err != nil {
		return nil, err
	}
	db := &Database{
		handle:  handle,
		writers: make(chan *lbug.Connection, 1),
		readers: make(chan *lbug.Connection, options.ReadConnections),
		options: options,
	}
	writer, err := lbug.OpenConnection(handle)
	if err != nil {
		handle.Close()
		return nil, err
	}
	db.writers <- writer
	db.connections = append(db.connections, writer)
	for range options.ReadConnections {
		connection, openErr := lbug.OpenConnection(handle)
		if openErr != nil {
			for len(db.readers) > 0 {
				(<-db.readers).Close()
			}
			writer.Close()
			handle.Close()
			return nil, openErr
		}
		db.readers <- connection
		db.connections = append(db.connections, connection)
	}
	return db, nil
}

func normalizeOptions(options Options) Options {
	if options.ReadConnections <= 0 {
		options.ReadConnections = defaultReadConnections
	} else if options.ReadConnections > maxReadConnections {
		options.ReadConnections = maxReadConnections
	}
	if options.QueryTimeout <= 0 {
		options.QueryTimeout = defaultQueryTimeout
	}
	if options.InterruptGrace <= 0 {
		options.InterruptGrace = defaultInterruptGrace
	}
	return options
}

func (db *Database) Close() error {
	db.closeOnce.Do(func() {
		db.operations.Lock()
		defer db.operations.Unlock()
		db.closed.Store(true)
		// queries.Wait() first: reclaimers block until their abandoned
		// statement returns, so they cannot finish before the queries do.
		db.queries.Wait()
		db.reclaims.Wait()
		close(db.readers)
		close(db.writers)
		db.connectionsMu.Lock()
		defer db.connectionsMu.Unlock()
		for _, connection := range db.connections {
			connection.Close()
		}
		db.handle.Close()
	})
	return nil
}

func (db *Database) EnsureSchema(ctx context.Context) error {
	return db.update(ctx, defaultQueryTimeout, func(session *Session) error {
		return ensureSchema(ctx, session)
	})
}

func (db *Database) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db.closed.Load() {
		return errClosed
	}
	if db.unhealthy.Load() {
		return errUnhealthy
	}
	return nil
}

func (db *Database) View(ctx context.Context, fn func(*Session) error) (err error) {
	db.operations.RLock()
	defer db.operations.RUnlock()
	if err := db.Health(ctx); err != nil {
		return err
	}
	var connection *lbug.Connection
	select {
	case connection = <-db.readers:
	case <-ctx.Done():
		return ctx.Err()
	}
	session := db.session(connection, db.readers)
	if err := executeStatement(ctx, session, `BEGIN TRANSACTION READ ONLY`); err != nil {
		// BEGIN can land natively and still report the context cancellation that
		// raced it, which would put a connection holding an open transaction back
		// into the pool. Rolling back unconditionally closes that window: with no
		// transaction open the statement fails harmlessly and the connection stays
		// usable, and a quarantined session skips it as unhealthy.
		cleanupCtx, cancelCleanup := transactionCleanupContext(ctx, session.timeout)
		_ = executeStatement(cleanupCtx, session, `ROLLBACK`)
		cancelCleanup()
		session.invalidate()
		if session.reusable.Load() {
			db.readers <- connection
		} else if !session.quarantined.Load() {
			db.replaceConnection(connection, db.readers, nil)
		}
		return err
	}
	defer func() {
		panicValue := recover()
		if session.reusable.Load() {
			cleanupCtx, cancel := transactionCleanupContext(ctx, session.timeout)
			rollbackErr := executeStatement(cleanupCtx, session, `ROLLBACK`)
			cancel()
			if err == nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					err = ctxErr
				} else {
					err = rollbackErr
				}
			}
			if rollbackErr != nil {
				session.reusable.Store(false)
			}
		}
		session.invalidate()
		if session.reusable.Load() {
			db.readers <- connection
		} else if !session.quarantined.Load() {
			db.replaceConnection(connection, db.readers, nil)
		}
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	err = fn(session)
	return err
}

func (db *Database) Update(ctx context.Context, fn func(*Session) error) (err error) {
	return db.update(ctx, db.options.QueryTimeout, fn)
}

func (db *Database) update(ctx context.Context, timeout time.Duration, fn func(*Session) error) (err error) {
	db.operations.RLock()
	defer db.operations.RUnlock()
	if err := db.Health(ctx); err != nil {
		return err
	}
	var connection *lbug.Connection
	select {
	case connection = <-db.writers:
	case <-ctx.Done():
		return ctx.Err()
	}
	if connection == nil {
		return errClosed
	}
	session := db.session(connection, db.writers)
	session.timeout = timeout
	if err := executeStatement(ctx, session, `BEGIN TRANSACTION`); err != nil {
		// BEGIN can land natively and still report the context cancellation that
		// raced it, which would put a connection holding an open transaction back
		// into the pool. Rolling back unconditionally closes that window: with no
		// transaction open the statement fails harmlessly and the connection stays
		// usable, and a quarantined session skips it as unhealthy.
		cleanupCtx, cancelCleanup := transactionCleanupContext(ctx, session.timeout)
		_ = executeStatement(cleanupCtx, session, `ROLLBACK`)
		cancelCleanup()
		session.invalidate()
		if session.reusable.Load() {
			db.writers <- connection
		} else if !session.quarantined.Load() {
			db.replaceConnection(connection, db.writers, nil)
		}
		return err
	}
	defer func() {
		panicValue := recover()
		committed := false
		if panicValue == nil && err == nil && ctx.Err() == nil && session.reusable.Load() {
			err = executeStatement(ctx, session, `COMMIT`)
			committed = err == nil
		}
		if !committed && session.reusable.Load() {
			cleanupCtx, cancel := transactionCleanupContext(ctx, session.timeout)
			rollbackErr := executeStatement(cleanupCtx, session, `ROLLBACK`)
			cancel()
			if err == nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					err = ctxErr
				} else {
					err = rollbackErr
				}
			}
			if rollbackErr != nil {
				session.reusable.Store(false)
			}
		}
		session.invalidate()
		// A reusable connection goes straight back into the slot. Otherwise the
		// reclaimer that quarantined it owns the return, so the slot stays empty
		// and later writers block instead of touching a dirty connection.
		if session.reusable.Load() {
			db.writers <- connection
		} else if !session.quarantined.Load() {
			db.replaceConnection(connection, db.writers, nil)
		}
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	err = fn(session)
	return err
}

func (db *Database) session(connection *lbug.Connection, pool chan *lbug.Connection) *Session {
	session := &Session{
		connection:     connection,
		timeout:        db.options.QueryTimeout,
		interruptGrace: db.options.InterruptGrace,
		database:       db,
		pool:           pool,
		active:         true,
	}
	session.reusable.Store(true)
	return session
}

// replaceConnection retires a connection that can no longer be trusted and puts
// a fresh one back into pool, so a single poisoned connection costs one
// connection rather than the whole database.
//
// When drained is non-nil the connection is still executing an abandoned
// statement: closing it before that statement returns would free state the
// native library is using, so the reclaimer waits first. Waiting also rolls the
// abandoned transaction back, because Ladybug discards a connection's open
// transaction when the connection closes.
func (db *Database) replaceConnection(connection *lbug.Connection, pool chan *lbug.Connection, drained <-chan error) {
	if pool == nil {
		// The connection belongs to the caller (package-level EnsureSchema),
		// not to a pool this database owns, so it is not ours to retire.
		return
	}
	db.reclaims.Add(1)
	go func() {
		defer db.reclaims.Done()
		if drained != nil {
			<-drained
		}
		connection.Close()
		db.connectionsMu.Lock()
		db.connections = slices.DeleteFunc(db.connections, func(candidate *lbug.Connection) bool {
			return candidate == connection
		})
		db.connectionsMu.Unlock()
		if db.closed.Load() {
			return
		}
		fresh, err := lbug.OpenConnection(db.handle)
		if err != nil {
			// The database itself is refusing connections; this is the one
			// condition the caller cannot recover from by retrying.
			db.unhealthy.Store(true)
			return
		}
		db.connectionsMu.Lock()
		db.connections = append(db.connections, fresh)
		db.connectionsMu.Unlock()
		pool <- fresh
	}()
}

func transactionCleanupContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}
