package ladybug

import (
	"context"
	"errors"
	"fmt"
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
	handle      *lbug.Database
	writer      *lbug.Connection
	readers     chan *lbug.Connection
	connections []*lbug.Connection
	options     Options
	writeMu     sync.Mutex
	operations  sync.RWMutex
	queries     sync.WaitGroup
	closeOnce   sync.Once
	closed      atomic.Bool
	unhealthy   atomic.Bool
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
		readers: make(chan *lbug.Connection, options.ReadConnections),
		options: options,
	}
	db.writer, err = lbug.OpenConnection(handle)
	if err != nil {
		handle.Close()
		return nil, err
	}
	db.connections = append(db.connections, db.writer)
	for range options.ReadConnections {
		connection, openErr := lbug.OpenConnection(handle)
		if openErr != nil {
			for len(db.readers) > 0 {
				(<-db.readers).Close()
			}
			db.writer.Close()
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
		db.queries.Wait()
		close(db.readers)
		for _, connection := range db.connections {
			connection.Close()
		}
		db.handle.Close()
	})
	return nil
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

func (db *Database) View(ctx context.Context, fn func(*Session) error) error {
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
	session := db.session(connection)
	defer func() {
		if session.reusable.Load() {
			db.readers <- connection
		}
	}()
	defer session.invalidate()
	if err := executeAndClose(connection, `BEGIN TRANSACTION READ ONLY`); err != nil {
		return err
	}
	err := fn(session)
	session.invalidate()
	if !session.reusable.Load() {
		return err
	}
	if rollbackErr := executeAndClose(connection, `ROLLBACK`); err == nil {
		err = rollbackErr
	}
	return err
}

func (db *Database) Update(ctx context.Context, fn func(*Session) error) error {
	db.operations.RLock()
	defer db.operations.RUnlock()
	if err := db.Health(ctx); err != nil {
		return err
	}
	db.writeMu.Lock()
	defer db.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	session := db.session(db.writer)
	defer session.invalidate()
	if err := executeAndClose(db.writer, `BEGIN TRANSACTION`); err != nil {
		return err
	}
	err := fn(session)
	session.invalidate()
	if !session.reusable.Load() {
		return err
	}
	statement := `COMMIT`
	if err != nil {
		statement = `ROLLBACK`
	}
	if transactionErr := executeAndClose(db.writer, statement); err == nil {
		err = transactionErr
	}
	return err
}

func (db *Database) session(connection *lbug.Connection) *Session {
	session := &Session{
		connection:     connection,
		timeout:        db.options.QueryTimeout,
		interruptGrace: db.options.InterruptGrace,
		database:       db,
		active:         true,
	}
	session.reusable.Store(true)
	return session
}

func executeAndClose(connection *lbug.Connection, query string) error {
	result, err := connection.Query(query)
	if result != nil {
		result.Close()
	}
	if err != nil {
		return fmt.Errorf("%s: %w", query, err)
	}
	return nil
}
