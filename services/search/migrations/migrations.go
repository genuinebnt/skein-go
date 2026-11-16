// Package migrations embeds the search schema's SQL migrations, applied at
// startup by platform/postgres.Migrate.
package migrations

import "embed"

// FS holds the migration files, named NNNN_description.sql.
//
//go:embed *.sql
var FS embed.FS
