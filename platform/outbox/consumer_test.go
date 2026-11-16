package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/genuinebnt/skein-go/platform/events"
)

// fakeExecer captures the statements Enqueue runs.
type fakeExecer struct {
	sql  []string
	args [][]any
}

func (f *fakeExecer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.sql = append(f.sql, sql)
	f.args = append(f.args, args)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func TestEnqueueStoresTheEnvelopeWithTheRequestsTrace(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "PUT /v1/notes/1")
	defer span.End()

	e := events.Envelope{ID: "0193a1b2-0000-7000-8000-000000000001", Source: "documents", OrderingKey: "n1",
		Payload: []byte{1, 2}, Attributes: map[string]string{"kind": "note_renamed"}}
	db := &fakeExecer{}
	if err := Enqueue(ctx, db, "documents.events", e); err != nil {
		t.Fatal(err)
	}
	args := db.args[0]
	if args[0] != "documents.events" || args[1] != e.ID || args[2] != "documents" || args[3] != "n1" {
		t.Fatalf("args %v", args)
	}
	if tp, _ := args[4].(string); !strings.Contains(tp, span.SpanContext().TraceID().String()) {
		t.Fatalf("traceparent %q does not carry the request's trace", tp)
	}
	var attrs map[string]string
	if err := json.Unmarshal(args[5].([]byte), &attrs); err != nil || attrs["kind"] != "note_renamed" {
		t.Fatalf("attributes %s", args[5])
	}
}

func TestEnqueueRejectsInvalidEvents(t *testing.T) {
	valid := events.Envelope{ID: "x", Source: "documents"}
	for name, c := range map[string]struct {
		topic string
		e     events.Envelope
	}{
		"no topic":      {"", valid},
		"no id":         {"t", events.Envelope{Source: "documents"}},
		"reserved attr": {"t", events.Envelope{ID: "x", Source: "s", Attributes: map[string]string{"traceparent": "x"}}},
	} {
		if err := Enqueue(context.Background(), &fakeExecer{}, c.topic, c.e); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// fakeDB is a transactional store for processed events and one effect
// counter, enough to check that HandleOnce's record and effect commit or
// roll back together.
type fakeDB struct {
	processed map[string]bool
	effects   int
}

type fakeTx struct {
	pgx.Tx
	db        *fakeDB
	processed []string
	effects   int
	done      bool
}

func (d *fakeDB) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return &fakeTx{db: d}, nil
}

func (tx *fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "processed_events") {
		key := args[0].(string) + "/" + args[1].(string)
		if tx.db.processed[key] || slices.Contains(tx.processed, key) {
			return pgconn.NewCommandTag("INSERT 0 0"), nil
		}
		tx.processed = append(tx.processed, key)
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}
	tx.effects++
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (tx *fakeTx) Commit(context.Context) error {
	tx.done = true
	for _, k := range tx.processed {
		tx.db.processed[k] = true
	}
	tx.db.effects += tx.effects
	return nil
}

func (tx *fakeTx) Rollback(context.Context) error {
	if tx.done {
		return pgx.ErrTxClosed
	}
	tx.done = true
	return nil
}

func applyEffect(ctx context.Context, tx pgx.Tx, _ events.Message) error {
	_, err := tx.Exec(ctx, "UPDATE search_index SET ...")
	return err
}

func TestDuplicateDeliveryAppliesTheEffectOnce(t *testing.T) {
	db := &fakeDB{processed: map[string]bool{}}
	c, _ := NewConsumer(db, "search.documents-events", nil)
	msg := events.Message{Envelope: events.Envelope{ID: "evt-1", Source: "documents"}}
	h := c.Handler(applyEffect)
	for range 3 {
		if err := h(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	if db.effects != 1 {
		t.Fatalf("effect applied %d times, want 1", db.effects)
	}
}

func TestFailedEffectIsRetriedOnRedelivery(t *testing.T) {
	db := &fakeDB{processed: map[string]bool{}}
	c, _ := NewConsumer(db, "search.documents-events", nil)
	msg := events.Message{Envelope: events.Envelope{ID: "evt-1", Source: "documents"}}
	failing := func(ctx context.Context, tx pgx.Tx, m events.Message) error {
		_ = applyEffect(ctx, tx, m)
		return errors.New("index unavailable")
	}
	if err := c.HandleOnce(context.Background(), msg, failing); err == nil {
		t.Fatal("effect error not returned (the message would be acked)")
	}
	if db.processed["search.documents-events/evt-1"] || db.effects != 0 {
		t.Fatal("a failed effect left its processed record or partial effect behind")
	}
	if err := c.HandleOnce(context.Background(), msg, applyEffect); err != nil || db.effects != 1 {
		t.Fatalf("redelivery: err %v effects %d", err, db.effects)
	}
}

func TestConsumersAreIndependent(t *testing.T) {
	db := &fakeDB{processed: map[string]bool{}}
	search, _ := NewConsumer(db, "search.documents-events", nil)
	ml, _ := NewConsumer(db, "ml.documents-events", nil)
	msg := events.Message{Envelope: events.Envelope{ID: "evt-1", Source: "documents"}}
	_ = search.HandleOnce(context.Background(), msg, applyEffect)
	_ = ml.HandleOnce(context.Background(), msg, applyEffect)
	if db.effects != 2 {
		t.Fatalf("effects %d: one consumer's record must not suppress another's", db.effects)
	}
}
