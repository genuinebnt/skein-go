package postgres_test

import (
	"fmt"
	"testing/fstest"

	"github.com/genuinebnt/skein-go/platform/postgres"
)

// A service embeds its migrations (//go:embed migrations/*.sql) and calls
// postgres.Migrate(ctx, pool, migrationsFS, "migrations", log) at startup.
// Planning is shown here because it needs no database.
func Example() {
	files := fstest.MapFS{
		"migrations/0001_notes.sql":    {Data: []byte("CREATE TABLE notes (id uuid PRIMARY KEY);")},
		"migrations/0002_outbox.sql":   {Data: []byte("-- copied from outbox.SchemaSQL")},
		"migrations/0003_comments.sql": {Data: []byte("CREATE TABLE comments (id uuid PRIMARY KEY);")},
	}
	available, _ := postgres.LoadMigrations(files, "migrations")
	applied := []postgres.AppliedMigration{{Version: 1, Checksum: available[0].Checksum}}
	pending, err := postgres.Plan(available, applied)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, m := range pending {
		fmt.Printf("%04d_%s\n", m.Version, m.Name)
	}
	// Output:
	// 0002_outbox
	// 0003_comments
}
