// Command notifications-seed loads the notification service's share of a
// seed dataset and checks the producer rules against it
// (docs/seed-data.md, internal/seed):
//
//	go run ./services/notifications/cmd/notifications-seed -dir testdata/seed/small
//
// It always checks the dataset's manifest, then replays the dataset's
// events through the real consumers into an in-memory store and compares
// the result with notifications.jsonl (set equality on user, kind and
// source), exiting 1 on any difference. Unless -check-only is given it then
// migrates the database at DATABASE_URL and loads notifications.jsonl into
// it, keeping the dataset's IDs and read flags; loading twice changes
// nothing. No email is sent or enqueued.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/postgres"
	pgadapter "github.com/genuinebnt/skein-go/services/notifications/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/notifications/internal/fakes"
	"github.com/genuinebnt/skein-go/services/notifications/internal/seed"
	"github.com/genuinebnt/skein-go/services/notifications/migrations"
)

func main() {
	dir := flag.String("dir", "", "seed dataset directory (holds manifest.json)")
	checkOnly := flag.Bool("check-only", false, "only check the rules in memory; do not touch the database")
	flag.Parse()
	logger := logging.New(logging.Options{Service: "notifications-seed"})
	if err := run(context.Background(), *dir, *checkOnly, logger); err != nil {
		logger.Error("seed failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, dir string, checkOnly bool, logger *slog.Logger) error {
	if dir == "" {
		return errors.New("-dir is required")
	}
	d, err := seed.Open(dir)
	if err != nil {
		return err
	}
	logger.Info("dataset accepted", slog.String("dir", dir), slog.Int("notifications", len(d.Notifications)),
		slog.Time("reference_time", d.ReferenceTime))

	memory := fakes.NewStore()
	stats, err := seed.Replay(ctx, d, seed.ReplayConfig{Store: memory, NewID: fakes.IDs(1), Logger: logger})
	if err != nil {
		return err
	}
	diff := seed.Compare(d.Notifications, memory.Notifications())
	if !diff.OK() {
		return fmt.Errorf("producer rules disagree with notifications.jsonl: %s", diff)
	}
	logger.Info("producer rules match the expected inbox", slog.Int("events", stats.Events), slog.Int("notifications", diff.Got))
	if checkOnly {
		return nil
	}

	l := config.FromEnv()
	dbCfg := postgres.LoadConfig(l)
	if err := l.Err(); err != nil {
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
	loaded, err := seed.LoadInbox(ctx, pgadapter.NewStore(pool), d)
	if err != nil {
		return err
	}
	logger.Info("inbox loaded", slog.Int("loaded", loaded.Loaded), slog.Int("already_present", loaded.AlreadyPresent),
		slog.String("preferences", "none in the dataset; every user has the defaults"))
	return nil
}
