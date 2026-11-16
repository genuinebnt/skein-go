// Package migrations embeds the notification service's versioned SQL,
// applied at startup by platform/postgres.Migrate (forward-only, with
// checksums). Never edit an applied file; add a new one.
package migrations

import "embed"

// FS holds every NNNN_description.sql file of this directory.
//
//go:embed *.sql
var FS embed.FS
