// Command search-seed loads the search service's share of a seed dataset
// (docs/seed-data.md, "Loader contract"):
//
//	go run ./services/search/cmd/search-seed -dir testdata/seed/small
//	go run ./services/search/cmd/search-seed -dir testdata/seed/small -dry-run
//
// It reads notes, blocks and links, feeds them through the projector as
// documents events (idempotently: a second run applies nothing), restores
// the projection, checks its invariants and writes a snapshot. With
// -dry-run it uses the in-memory store and needs no database. It never
// reads truth/; the tests in internal/seed compare the loaded projection
// with it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/postgres"
	pgadapter "github.com/genuinebnt/skein-go/services/search/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
	"github.com/genuinebnt/skein-go/services/search/internal/fakes"
	"github.com/genuinebnt/skein-go/services/search/internal/seed"
	"github.com/genuinebnt/skein-go/services/search/migrations"
)

func main() {
	dir := flag.String("dir", "testdata/seed/small", "seed dataset directory")
	dryRun := flag.Bool("dry-run", false, "load into memory only")
	flag.Parse()
	if err := run(*dir, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "search-seed:", err)
		os.Exit(1)
	}
}

func run(dir string, dryRun bool) error {
	ctx := context.Background()
	logger := logging.New(logging.Options{Service: "search-seed"})
	var store app.Store = fakes.NewStore()
	if !dryRun {
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
		store = pgadapter.NewStore(pool)
	}
	start := time.Now()
	stats, err := seed.Load(ctx, dir, app.NewProjector(store, nil, nil, logger), store)
	if err != nil {
		return err
	}
	index := app.NewIndex(store, app.IndexConfig{Logger: logger})
	if err := index.Bootstrap(ctx); err != nil {
		return err
	}
	var workspaces, visible, edges int
	if err := index.Read(func(p *domain.Projection) error {
		if err := p.CheckInvariants(); err != nil {
			return err
		}
		for _, id := range p.WorkspaceIDs() {
			w, _ := p.Workspace(id)
			g, err := w.LinkGraph(nil)
			if err != nil {
				return err
			}
			workspaces, visible, edges = workspaces+1, visible+g.Len(), edges+g.EdgeCount()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := index.Snapshot(ctx); err != nil {
		return err
	}
	fmt.Printf("loaded %d notes, %d blocks, %d links as %d events (%d new) in %s\n",
		stats.Notes, stats.Blocks, stats.Links, stats.Events, stats.Applied, time.Since(start).Round(time.Millisecond))
	fmt.Printf("projection at position %d: %d workspaces, %d visible notes, %d links in the graph; invariants hold; snapshot written\n",
		index.Position(), workspaces, visible, edges)
	return nil
}
