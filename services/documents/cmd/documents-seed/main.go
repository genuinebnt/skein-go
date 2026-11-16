// Command documents-seed loads the documents service's share of a seed
// dataset into its Postgres schema, following the loader contract in
// docs/seed-data.md, then verifies every note against the dataset's CRDT
// truth:
//
//	go run ./services/documents/cmd/documents-seed -dir testdata/seed/small
//
// It needs DATABASE_URL (the documents role, whose search_path is the
// documents schema). Events go to the outbox; the service's relay publishes
// them when it next runs. Permissions are decided as of each batch's
// delivery from the dataset's memberships and overrides (seed.Policy),
// because identity's Authorize answers only for the present. Loading twice
// changes nothing.
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/postgres"
	pgstore "github.com/genuinebnt/skein-go/services/documents/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/documents/internal/app"
	"github.com/genuinebnt/skein-go/services/documents/internal/fakes"
	"github.com/genuinebnt/skein-go/services/documents/internal/seed"
	"github.com/genuinebnt/skein-go/services/documents/migrations"
)

func main() {
	dir := flag.String("dir", "testdata/seed/small", "seed dataset directory")
	verify := flag.Bool("verify", true, "check every note against truth/crdt/final.jsonl after loading")
	flag.Parse()
	l := config.FromEnv()
	dbCfg := postgres.LoadConfig(l)
	if err := l.Err(); err != nil {
		log.Fatal(err)
	}
	logger := logging.New(logging.Options{Service: "documents-seed", Version: "dev", Env: "dev"})
	if err := run(context.Background(), logger, *dir, *verify, dbCfg); err != nil {
		logger.Error("seed failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, dir string, verify bool, dbCfg postgres.Config) error {
	d, err := seed.Open(dir)
	if err != nil {
		return err
	}
	pool, err := postgres.Open(ctx, dbCfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool, migrations.FS, ".", logger); err != nil {
		return err
	}
	clock := &seed.Clock{}
	clock.Set(d.ReferenceTime)
	policy, err := seed.NewPolicy(d, clock)
	if err != nil {
		return err
	}
	replicas, err := seed.NewReplicas(d)
	if err != nil {
		return err
	}
	// The loader needs no Redis: metadata caching and presence do not
	// matter to a one-off load, so in-memory versions stand in.
	svc := app.New(app.Deps{
		Store: pgstore.NewStore(pool), Authorizer: policy, Replicas: replicas,
		NoteCache: fakes.NewNoteCache(), Presence: fakes.NewPresence(clock), Clock: clock, Logger: logger,
	}, app.Config{Tick: time.Hour})
	started := time.Now()
	if _, err := seed.Load(ctx, d, svc, clock, logger); err != nil {
		return err
	}
	if err := svc.FlushAll(ctx); err != nil {
		return err
	}
	if verify {
		if err := seed.Verify(ctx, d, svc); err != nil {
			return err
		}
		logger.Info("seed verified against the CRDT truth")
	}
	logger.Info("seed done", "seconds", time.Since(started).Seconds())
	return nil
}
