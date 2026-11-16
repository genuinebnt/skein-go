// Package migrations embeds the documents schema's versioned SQL, applied at
// startup by platform/postgres.Migrate (forward-only, checksummed).
package migrations

import "embed"

// FS holds the migration files.
//
//go:embed *.sql
var FS embed.FS
