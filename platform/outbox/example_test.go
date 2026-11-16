package outbox_test

import (
	"fmt"
	"strings"

	"github.com/genuinebnt/skein-go/platform/outbox"
)

// A service writes its state change and the event in one transaction:
//
//	err := postgres.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
//	    if _, err := tx.Exec(ctx, `UPDATE notes SET title = $2 WHERE id = $1`, id, title); err != nil {
//	        return err
//	    }
//	    env, err := events.NewEnvelope(ctx, "documents", id, encodedNoteRenamed)
//	    if err != nil {
//	        return err
//	    }
//	    return outbox.Enqueue(ctx, tx, "documents.events", env)
//	})
//
// and runs one relay next to its servers:
//
//	relay, _ := outbox.NewRelay(outbox.NewPostgresStore(pool, outbox.PostgresStoreOptions{}), pubsub, outbox.RelayConfig{})
//	manager.Add("outbox-relay", relay.Run, nil)
//
// A consumer applies each event once:
//
//	consumer, _ := outbox.NewConsumer(pool, "search.documents-events", nil)
//	sub.Subscribe(ctx, "search.documents-events", consumer.Handler(reindex))
//
// The tables come from outbox.SchemaSQL, copied into a migration.
func Example() {
	fmt.Println(strings.Contains(outbox.SchemaSQL, "CREATE TABLE IF NOT EXISTS outbox ("))
	// Output: true
}
