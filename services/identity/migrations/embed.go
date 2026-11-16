// Package migrations embeds identity's forward-only SQL migrations, applied
// at startup by platform/postgres.Migrate into the identity schema (the
// identity role's search_path, deploy/compose/postgres/init.sql).
package migrations

import "embed"

// FS holds the NNNN_description.sql files.
//
//go:embed *.sql
var FS embed.FS
