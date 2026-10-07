package graphimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"

	"github.com/balcsida/graphnest/internal/graphartifact"
	_ "modernc.org/sqlite"
)

// Snapshot is one consistent read of a CodeGraph index. Rows keep rowid order.
type Snapshot struct {
	Path          string
	SchemaVersion int
	Nodes         []Node
	Edges         []Edge
	Files         []File
	Unresolved    []UnresolvedRef
	Metadata      []MetadataEntry
	// SynthesisInputs are the file paths of the synthesis_inputs table (schema 10 and 11); nil for schema 9.
	SynthesisInputs []string
	// RoundedTimestamps counts timestamp values CodeGraph stored as fractional
	// milliseconds (Node's mtimeMs kept as SQLite REAL) that were rounded.
	RoundedTimestamps int
}

// MetadataValue returns the project_metadata value stored under key.
func (s *Snapshot) MetadataValue(key string) (string, bool) {
	i := slices.IndexFunc(s.Metadata, func(m MetadataEntry) bool { return m.Key == key })
	if i < 0 {
		return "", false
	}
	return s.Metadata[i].Value, true
}

// millis scans a CodeGraph timestamp column. The columns are declared INTEGER,
// but CodeGraph stores Node's fractional mtimeMs unchanged and SQLite keeps
// such values as REAL; they are rounded to the nearest millisecond and counted.
type millis struct {
	value   int64
	rounded *int
}

func (m *millis) Scan(src any) error {
	switch v := src.(type) {
	case int64:
		m.value = v
	case float64:
		if v != math.Trunc(v) {
			*m.rounded++
		}
		m.value = int64(math.Round(v))
	default:
		return fmt.Errorf("timestamp column holds %T, want an integer or real number", src)
	}
	return nil
}

// Node mirrors the nodes table of schema 9.
type Node struct {
	ID, Kind, Name, QualifiedName, FilePath, Language string
	StartLine, EndLine, StartColumn, EndColumn        int64
	Docstring, Signature, Visibility                  *string
	IsExported, IsAsync, IsStatic, IsAbstract         *int64
	Decorators, TypeParameters, ReturnType            *string
	UpdatedAt                                         int64
}

// Edge mirrors the edges table of schema 9.
type Edge struct {
	ID                   int64
	Source, Target, Kind string
	Metadata             *string
	Line, Col            *int64
	Provenance           *string
}

// File mirrors the files table of schema 9.
type File struct {
	Path, ContentHash, Language string
	Size, ModifiedAt, IndexedAt int64
	NodeCount                   *int64
	Errors                      *string
	Generated                   int64
}

// UnresolvedRef mirrors the unresolved_refs table of schema 9.
type UnresolvedRef struct {
	ID                                       int64
	FromNodeID, ReferenceName, ReferenceKind string
	Line, Col                                int64
	Candidates                               *string
	FilePath, Language, Status, NameTail     string
}

// MetadataEntry mirrors the project_metadata table of schema 9.
type MetadataEntry struct {
	Key, Value string
	UpdatedAt  int64
}

// Read loads the CodeGraph index at path in one read-only transaction.
// It never creates, writes, checkpoints or locks the file for writing.
func Read(ctx context.Context, path string, limits graphartifact.Limits) (*Snapshot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(abs); err != nil {
		return nil, fmt.Errorf("open CodeGraph index: %w", err)
	}
	// The file: URI form is mandatory: the driver opens a plain path read-write.
	dsn := &url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, readError(ctx, err)
	}
	defer tx.Rollback()
	s, err := readSnapshot(ctx, tx, limits)
	if err != nil {
		return nil, readError(ctx, err)
	}
	s.Path = abs
	return s, nil
}

func readError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if notCodeGraph(err) && !errors.Is(err, ErrNotCodeGraph) {
		return fmt.Errorf("%w: %w", ErrNotCodeGraph, err)
	}
	return err
}

func readSnapshot(ctx context.Context, tx *sql.Tx, limits graphartifact.Limits) (*Snapshot, error) {
	version, err := schemaVersion(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err = checkTables(ctx, tx, version); err != nil {
		return nil, err
	}
	type counted struct {
		table      string
		limit, def int
	}
	tables := []counted{
		{"nodes", limits.MaxNodes, graphartifact.DefaultMaxNodes},
		{"edges", limits.MaxEdges, graphartifact.DefaultMaxEdges},
		{"files", limits.MaxFiles, graphartifact.DefaultMaxFiles},
		{"unresolved_refs", limits.MaxUnresolved, graphartifact.DefaultMaxUnresolved},
	}
	if version >= 10 {
		tables = append(tables, counted{"synthesis_inputs", limits.MaxFiles, graphartifact.DefaultMaxFiles})
	}
	for _, c := range tables {
		if c.limit == 0 {
			c.limit = c.def
		}
		var count int64
		if err = tx.QueryRowContext(ctx, `select count(*) from `+c.table).Scan(&count); err != nil {
			return nil, err
		}
		if count > int64(c.limit) {
			return nil, fmt.Errorf("%w: table %s has %d rows, limit is %d", ErrIndexTooLarge, c.table, count, c.limit)
		}
	}
	s := &Snapshot{SchemaVersion: version}
	if s.Nodes, err = scanRows(ctx, tx, `select id, kind, name, qualified_name, file_path, language, start_line, end_line, start_column, end_column, docstring, signature, visibility, is_exported, is_async, is_static, is_abstract, decorators, type_parameters, return_type, updated_at from nodes order by rowid`, func(r *sql.Rows, n *Node) error {
		updated := millis{rounded: &s.RoundedTimestamps}
		err := r.Scan(&n.ID, &n.Kind, &n.Name, &n.QualifiedName, &n.FilePath, &n.Language, &n.StartLine, &n.EndLine, &n.StartColumn, &n.EndColumn, &n.Docstring, &n.Signature, &n.Visibility, &n.IsExported, &n.IsAsync, &n.IsStatic, &n.IsAbstract, &n.Decorators, &n.TypeParameters, &n.ReturnType, &updated)
		n.UpdatedAt = updated.value
		return err
	}); err != nil {
		return nil, err
	}
	if s.Edges, err = scanRows(ctx, tx, `select id, source, target, kind, metadata, line, col, provenance from edges order by rowid`, func(r *sql.Rows, e *Edge) error {
		return r.Scan(&e.ID, &e.Source, &e.Target, &e.Kind, &e.Metadata, &e.Line, &e.Col, &e.Provenance)
	}); err != nil {
		return nil, err
	}
	if s.Files, err = scanRows(ctx, tx, `select path, content_hash, language, size, modified_at, indexed_at, node_count, errors, generated from files order by rowid`, func(r *sql.Rows, f *File) error {
		modified, indexed := millis{rounded: &s.RoundedTimestamps}, millis{rounded: &s.RoundedTimestamps}
		err := r.Scan(&f.Path, &f.ContentHash, &f.Language, &f.Size, &modified, &indexed, &f.NodeCount, &f.Errors, &f.Generated)
		f.ModifiedAt, f.IndexedAt = modified.value, indexed.value
		return err
	}); err != nil {
		return nil, err
	}
	if s.Unresolved, err = scanRows(ctx, tx, `select id, from_node_id, reference_name, reference_kind, line, col, candidates, file_path, language, status, name_tail from unresolved_refs order by rowid`, func(r *sql.Rows, u *UnresolvedRef) error {
		return r.Scan(&u.ID, &u.FromNodeID, &u.ReferenceName, &u.ReferenceKind, &u.Line, &u.Col, &u.Candidates, &u.FilePath, &u.Language, &u.Status, &u.NameTail)
	}); err != nil {
		return nil, err
	}
	if s.Metadata, err = scanRows(ctx, tx, `select key, value, updated_at from project_metadata order by rowid`, func(r *sql.Rows, m *MetadataEntry) error {
		updated := millis{rounded: &s.RoundedTimestamps}
		err := r.Scan(&m.Key, &m.Value, &updated)
		m.UpdatedAt = updated.value
		return err
	}); err != nil {
		return nil, err
	}
	if version >= 10 {
		if s.SynthesisInputs, err = scanRows(ctx, tx, `select file_path from synthesis_inputs order by rowid`, func(r *sql.Rows, p *string) error {
			return r.Scan(p)
		}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func scanRows[T any](ctx context.Context, tx *sql.Tx, query string, scan func(*sql.Rows, *T) error) ([]T, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var v T
		if err = scan(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
