package ladybug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
)

const (
	defaultMaxRows  = 1000
	defaultMaxBytes = 256 << 10
)

type QueryLimits struct {
	MaxRows  int
	MaxBytes int
}

type Result struct {
	Columns   []string
	Rows      [][]any
	Truncated bool
}

type Session struct {
	connection     *lbug.Connection
	timeout        time.Duration
	interruptGrace time.Duration
	database       *Database
	reusable       atomic.Bool
	executeMu      sync.Mutex
	active         bool
}

func (session *Session) Execute(ctx context.Context, query string, parameters map[string]any, limits QueryLimits) (Result, error) {
	var result Result
	err := session.executeNative(ctx, func() error {
		var err error
		result, err = executeQuery(session.connection, query, parameters, normalizeLimits(limits))
		return err
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (session *Session) executeNative(ctx context.Context, operation func() error) error {
	return session.executeNativeWithTimeout(ctx, session.timeout, operation)
}

func (session *Session) executeNativeWithTimeout(ctx context.Context, timeout time.Duration, operation func() error) error {
	session.executeMu.Lock()
	defer session.executeMu.Unlock()
	if !session.active {
		return errors.New("ladybug session callback has returned")
	}
	if !session.reusable.Load() {
		return errUnhealthy
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	timeoutMillis := timeout.Milliseconds()
	if timeoutMillis == 0 {
		timeoutMillis = 1
	}
	session.connection.SetTimeout(uint64(timeoutMillis))
	done := make(chan error, 1)
	session.database.queries.Add(1)
	go func() {
		defer session.database.queries.Done()
		done <- operation()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		session.connection.Interrupt()
	}
	timer := time.NewTimer(session.interruptGrace)
	defer timer.Stop()
	select {
	case <-done:
		return ctx.Err()
	case <-timer.C:
		session.reusable.Store(false)
		session.database.unhealthy.Store(true)
		return fmt.Errorf("%w: interrupt grace elapsed", ctx.Err())
	}
}

func executeStatement(ctx context.Context, session *Session, statement string) error {
	return executeStatementWithTimeout(ctx, session, session.timeout, statement)
}

func executeStatementWithTimeout(ctx context.Context, session *Session, timeout time.Duration, statement string) error {
	return session.executeNativeWithTimeout(ctx, timeout, func() error {
		result, err := session.connection.Query(statement)
		if result != nil {
			result.Close()
		}
		if err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
		return nil
	})
}

func (session *Session) invalidate() {
	session.executeMu.Lock()
	session.active = false
	session.executeMu.Unlock()
}

func normalizeLimits(limits QueryLimits) QueryLimits {
	if limits.MaxRows <= 0 || limits.MaxRows > defaultMaxRows {
		limits.MaxRows = defaultMaxRows
	}
	if limits.MaxBytes <= 0 || limits.MaxBytes > defaultMaxBytes {
		limits.MaxBytes = defaultMaxBytes
	}
	return limits
}

func executeQuery(connection *lbug.Connection, query string, parameters map[string]any, limits QueryLimits) (Result, error) {
	var (
		queryResult *lbug.QueryResult
		err         error
	)
	if parameters == nil {
		queryResult, err = connection.Query(query)
	} else {
		statement, prepareErr := connection.Prepare(query)
		if prepareErr != nil {
			return Result{}, prepareErr
		}
		defer statement.Close()
		queryResult, err = connection.Execute(statement, parameters)
	}
	if err != nil {
		return Result{}, err
	}
	defer queryResult.Close()
	result := Result{
		Columns: append([]string(nil), queryResult.GetColumnNames()...),
		Rows:    make([][]any, 0),
	}
	if encoded, encodeErr := json.Marshal(result); encodeErr != nil {
		return Result{}, encodeErr
	} else if len(encoded) > limits.MaxBytes {
		return Result{}, fmt.Errorf("query result metadata exceeds %d-byte limit", limits.MaxBytes)
	}
	for queryResult.HasNext() {
		if len(result.Rows) == limits.MaxRows {
			result.Truncated = true
			break
		}
		tuple, nextErr := queryResult.Next()
		if nextErr != nil {
			return Result{}, nextErr
		}
		row, rowErr := tuple.GetAsSlice()
		tuple.Close()
		if rowErr != nil {
			return Result{}, rowErr
		}
		candidate := result
		candidate.Rows = append(result.Rows, row)
		encoded, encodeErr := json.Marshal(candidate)
		if encodeErr != nil {
			return Result{}, encodeErr
		}
		if len(encoded) > limits.MaxBytes {
			result.Truncated = true
			break
		}
		result = candidate
	}
	return result, nil
}
