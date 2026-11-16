package redisx_test

import (
	"context"
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/platform/redisx"
)

type noteSummary struct {
	Title string `json:"title"`
	Words int    `json:"words"`
}

func Example() {
	ctx := context.Background()
	// In a service: redisx.NewRedisStore(client, "search").
	store := redisx.NewMemoryStore(nil)
	summaries, _ := redisx.New(store, redisx.Options[noteSummary]{Name: "summaries", TTL: 5 * time.Minute})

	load := func(context.Context) (noteSummary, []string, error) {
		fmt.Println("loading from documents")
		return noteSummary{Title: "Groceries", Words: 12}, []string{"note:0193"}, nil
	}
	s, _ := summaries.GetOrLoad(ctx, "0193", load)
	s, _ = summaries.GetOrLoad(ctx, "0193", load) // cached
	fmt.Println(s.Title, s.Words)

	// A documents.events consumer sees NoteRenamed for 0193.
	inv, _ := redisx.NewInvalidator(store, nil)
	n, _ := inv.InvalidateTags(ctx, "note:0193")
	fmt.Println("invalidated", n)
	// Output:
	// loading from documents
	// Groceries 12
	// invalidated 1
}
