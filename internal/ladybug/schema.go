package ladybug

import (
	"context"

	lbug "github.com/LadybugDB/go-ladybug"
)

const (
	SchemaVersion = 1
	NativeVersion = "0.18.3"
)

var schemaStatements = []string{
	`CREATE NODE TABLE IF NOT EXISTS GraphMetadata(id INT64, schema_version INT32, native_version STRING, PRIMARY KEY(id))`,
	`CREATE NODE TABLE IF NOT EXISTS Repository(id INT64, name STRING, commit STRING, upload_id INT64, schema_version INT32, source STRING, content_hash BLOB, PRIMARY KEY(id))`,
	`CREATE NODE TABLE IF NOT EXISTS File(uid STRING, repository_id INT64, path STRING, PRIMARY KEY(uid))`,
	`CREATE NODE TABLE IF NOT EXISTS Symbol(uid STRING, repository_id INT64, path STRING, language STRING, kind STRING, qualified_name STRING, signature STRING, start_line INT32, start_character INT32, end_line INT32, end_character INT32, PRIMARY KEY(uid))`,
	`CREATE REL TABLE IF NOT EXISTS CONTAINS(FROM Repository TO File, FROM File TO Symbol, path STRING, start_line INT32, start_character INT32, end_line INT32, end_character INT32, confidence DOUBLE, resolution_reason STRING)`,
	`CREATE REL TABLE IF NOT EXISTS IMPORTS(FROM File TO File, path STRING, start_line INT32, start_character INT32, end_line INT32, end_character INT32, confidence DOUBLE, resolution_reason STRING)`,
	`CREATE REL TABLE IF NOT EXISTS REFERENCES(FROM Symbol TO Symbol, path STRING, start_line INT32, start_character INT32, end_line INT32, end_character INT32, confidence DOUBLE, resolution_reason STRING)`,
	`CREATE REL TABLE IF NOT EXISTS CALLS(FROM Symbol TO Symbol, path STRING, start_line INT32, start_character INT32, end_line INT32, end_character INT32, confidence DOUBLE, resolution_reason STRING)`,
	`CREATE REL TABLE IF NOT EXISTS EXTENDS(FROM Symbol TO Symbol, path STRING, start_line INT32, start_character INT32, end_line INT32, end_character INT32, confidence DOUBLE, resolution_reason STRING)`,
	`CREATE REL TABLE IF NOT EXISTS IMPLEMENTS(FROM Symbol TO Symbol, path STRING, start_line INT32, start_character INT32, end_line INT32, end_character INT32, confidence DOUBLE, resolution_reason STRING)`,
}

func (db *Database) WriteCompatibility(ctx context.Context) error {
	return db.Update(ctx, func(session *Session) error {
		_, err := session.Execute(ctx, `MERGE (m:GraphMetadata {id: 1}) SET m.schema_version = $schema_version, m.native_version = $native_version`,
			map[string]any{"schema_version": int32(SchemaVersion), "native_version": NativeVersion}, QueryLimits{})
		return err
	})
}

func (db *Database) Compatible(ctx context.Context) (bool, error) {
	var compatible bool
	err := db.View(ctx, func(session *Session) error {
		result, err := session.Execute(ctx, `MATCH (m:GraphMetadata {id: 1}) RETURN m.schema_version, m.native_version`, nil, QueryLimits{MaxRows: 2})
		if err != nil || len(result.Rows) != 1 {
			return err
		}
		var schema int64
		switch value := result.Rows[0][0].(type) {
		case int32:
			schema = int64(value)
		case int64:
			schema = value
		}
		native, nativeOK := result.Rows[0][1].(string)
		compatible = nativeOK && schema == SchemaVersion && native == NativeVersion
		return nil
	})
	return compatible, err
}

func EnsureSchema(ctx context.Context, connection *lbug.Connection) error {
	db := &Database{options: normalizeOptions(Options{})}
	session := db.session(connection)
	defer session.invalidate()
	err := ensureSchema(ctx, session)
	db.queries.Wait()
	return err
}

func ensureSchema(ctx context.Context, session *Session) error {
	for _, statement := range schemaStatements {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := executeStatementWithTimeout(ctx, session, defaultQueryTimeout, statement); err != nil {
			return err
		}
	}
	return nil
}
