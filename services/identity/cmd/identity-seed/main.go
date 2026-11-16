// Command identity-seed loads identity's share of a seed dataset into the
// identity schema (docs/seed-data.md, "Loader contract"):
//
//	DATABASE_URL=postgres://identity:identity@localhost:5432/skein?sslmode=disable \
//	  go run ./services/identity/cmd/identity-seed -dir testdata/seed/small
//
// It applies identity's migrations first, so it works on an empty
// database, and is idempotent. Set SKEIN_SEED_PASSWORD to give every seed
// user that password (local stacks only; the variable is never printed).
// Run the service with IDENTITY_FIXED_NOW set to the dataset's
// reference_time (printed at the end) so expiry and removal checks match
// the ground truth.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/postgres"

	identitypg "github.com/genuinebnt/skein-go/services/identity/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/seed"
	"github.com/genuinebnt/skein-go/services/identity/migrations"
)

func main() {
	dir := flag.String("dir", "", "dataset directory (holds manifest.json)")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "identity-seed: -dir is required")
		os.Exit(2)
	}
	l := config.FromEnv()
	dbCfg := postgres.LoadConfig(l)
	password := l.OptionalSecret("SKEIN_SEED_PASSWORD")
	if err := l.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "identity-seed:", err)
		os.Exit(2)
	}
	if err := run(context.Background(), *dir, dbCfg, password); err != nil {
		fmt.Fprintln(os.Stderr, "identity-seed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dir string, dbCfg postgres.Config, password config.Secret) error {
	opts := seed.Options{Dir: dir}
	if password.IsSet() {
		hasher, err := credential.NewHasher(credential.HasherOptions{})
		if err != nil {
			return err
		}
		if opts.PasswordHash, err = hasher.Hash(ctx, password.Reveal()); err != nil {
			return err
		}
	}
	pool, err := postgres.Open(ctx, dbCfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool, migrations.FS, ".", logging.Discard()); err != nil {
		return err
	}
	rep, err := seed.Load(ctx, identitypg.NewStore(pool), opts)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(rep)
}
