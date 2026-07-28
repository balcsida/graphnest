package ladybug

import (
	"context"

	lbug "github.com/LadybugDB/go-ladybug"
)

const SchemaVersion = 1

var schemaStatements = []string{
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

func EnsureSchema(ctx context.Context, connection *lbug.Connection) error {
	for _, statement := range schemaStatements {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := connection.Query(statement)
		if err != nil {
			return err
		}
		result.Close()
	}
	return nil
}
