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

type queryOutcome struct {
	result Result
	err    error
}

func (session *Session) Execute(ctx context.Context, query string, parameters map[string]any, limits QueryLimits) (Result, error) {
	session.executeMu.Lock()
	defer session.executeMu.Unlock()
	if !session.active {
		return Result{}, errors.New("ladybug session callback has returned")
	}
	if !session.reusable.Load() {
		return Result{}, errUnhealthy
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, session.timeout)
	defer cancel()
	timeout := session.timeout.Milliseconds()
	if timeout == 0 {
		timeout = 1
	}
	session.connection.SetTimeout(uint64(timeout))
	done := make(chan queryOutcome, 1)
	session.database.queries.Add(1)
	go func() {
		defer session.database.queries.Done()
		result, err := executeQuery(session.connection, query, parameters, normalizeLimits(limits))
		done <- queryOutcome{result: result, err: err}
	}()
	select {
	case outcome := <-done:
		return outcome.result, outcome.err
	case <-ctx.Done():
		session.connection.Interrupt()
	}
	timer := time.NewTimer(session.interruptGrace)
	defer timer.Stop()
	select {
	case <-done:
		return Result{}, ctx.Err()
	case <-timer.C:
		session.reusable.Store(false)
		session.database.unhealthy.Store(true)
		return Result{}, fmt.Errorf("%w: interrupt grace elapsed", ctx.Err())
	}
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
