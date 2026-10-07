package graphimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Errors returned by Read. Detail is wrapped with %w, so match with errors.Is.
var (
	ErrNotCodeGraph      = errors.New("not a CodeGraph index")
	ErrUnsupportedSchema = errors.New("unsupported CodeGraph schema version")
	ErrIndexTooLarge     = errors.New("CodeGraph index exceeds import limits")
)

// SupportedSchemaVersions lists the CodeGraph schema versions this reader accepts (9 = CodeGraph 1.6.0).
var SupportedSchemaVersions = []int{9}

// requiredColumns are the tables and columns of schema version 9 that Read selects.
var requiredColumns = map[string][]string{
	"nodes":            {"id", "kind", "name", "qualified_name", "file_path", "language", "start_line", "end_line", "start_column", "end_column", "docstring", "signature", "visibility", "is_exported", "is_async", "is_static", "is_abstract", "decorators", "type_parameters", "return_type", "updated_at"},
	"edges":            {"id", "source", "target", "kind", "metadata", "line", "col", "provenance"},
	"files":            {"path", "content_hash", "language", "size", "modified_at", "indexed_at", "node_count", "errors", "generated"},
	"unresolved_refs":  {"id", "from_node_id", "reference_name", "reference_kind", "line", "col", "candidates", "file_path", "language", "status", "name_tail"},
	"project_metadata": {"key", "value", "updated_at"},
}

// notCodeGraph reports whether a driver error means the file is not a CodeGraph database.
func notCodeGraph(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "file is not a database") || strings.Contains(msg, "malformed database schema")
}

func schemaVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var version sql.NullInt64
	if err := tx.QueryRowContext(ctx, `select max(version) from schema_versions`).Scan(&version); err != nil {
		if notCodeGraph(err) {
			return 0, fmt.Errorf("%w: %w", ErrNotCodeGraph, err)
		}
		return 0, err
	}
	if !version.Valid {
		return 0, fmt.Errorf("%w: schema_versions is empty", ErrNotCodeGraph)
	}
	v := int(version.Int64)
	if slices.Contains(SupportedSchemaVersions, v) {
		return v, nil
	}
	newest := slices.Max(SupportedSchemaVersions)
	if v > newest {
		return 0, fmt.Errorf("%w: CodeGraph schema version %d is newer than this graphnest supports (%d); update graphnest", ErrUnsupportedSchema, v, newest)
	}
	return 0, fmt.Errorf("%w: CodeGraph schema version %d is older than this graphnest supports (%d); re-index with CodeGraph 1.6.0 or newer", ErrUnsupportedSchema, v, SupportedSchemaVersions[0])
}

func checkTables(ctx context.Context, tx *sql.Tx) error {
	for table, want := range requiredColumns {
		have := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `select name from pragma_table_info(?)`, table)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			have[name] = true
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(have) == 0 {
			return fmt.Errorf("%w: missing table %s", ErrNotCodeGraph, table)
		}
		for _, column := range want {
			if !have[column] {
				return fmt.Errorf("%w: table %s is missing column %s", ErrNotCodeGraph, table, column)
			}
		}
	}
	return nil
}
