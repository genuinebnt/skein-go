//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/genuinebnt/skein-go/platform/config"
	platformpg "github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/platform/postgres/pgtest"
	pgadapter "github.com/genuinebnt/skein-go/services/notifications/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/fakes"
	"github.com/genuinebnt/skein-go/services/notifications/internal/seed"
	"github.com/genuinebnt/skein-go/services/notifications/migrations"
)

// startPostgres runs the compose stack's image, applies the migrations
// (twice, to prove they are idempotent) and returns a store.
func startPostgres(t *testing.T) (*pgadapter.Store, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	// SKEIN_TEST_POSTGRES_DSN runs against an existing server (make
	// up-native) instead of a container; see platform/postgres/pgtest.
	url, ok := pgtest.FreshDatabase(t)
	if !ok {
		container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
			tcpostgres.WithDatabase("skein"), tcpostgres.WithUsername("skein"), tcpostgres.WithPassword("skein"),
			tcpostgres.BasicWaitStrategies())
		if err != nil {
			t.Fatalf("start postgres: %v", err)
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		if url, err = container.ConnectionString(ctx, "sslmode=disable"); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := platformpg.Open(ctx, platformpg.Config{URL: config.NewSecret(url)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for range 2 {
		if err := platformpg.Migrate(ctx, pool, migrations.FS, ".", nil); err != nil {
			t.Fatal(err)
		}
	}
	return pgadapter.NewStore(pool), pool
}

func newID() string { return uuid.Must(uuid.NewV7()).String() }

func TestSeedReplayOnPostgresReproducesTheExpectedInbox(t *testing.T) {
	store, _ := startPostgres(t)
	d, err := seed.Open("../../../../../testdata/seed/small")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := seed.Replay(ctx, d, seed.ReplayConfig{Store: store, NewID: newID}); err != nil {
		t.Fatal(err)
	}
	var produced []domain.Notification
	for _, u := range d.Users {
		err := store.WithTx(ctx, func(ctx context.Context, tx app.Tx) error {
			items, err := tx.ListInbox(ctx, app.InboxQuery{RecipientID: u.ID, Limit: 10000})
			produced = append(produced, items...)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if diff := seed.Compare(d.Notifications, produced); !diff.OK() {
		t.Fatalf("Postgres store disagrees with the seed: %s", diff)
	}
	stats, err := seed.Replay(ctx, d, seed.ReplayConfig{Store: store, NewID: newID})
	if err != nil || stats.Duplicates != stats.Events {
		t.Fatalf("second replay: %+v, %v", stats, err)
	}
}

func TestSeedInboxLoadsOnceAndKeepsIDsOnPostgres(t *testing.T) {
	store, pool := startPostgres(t)
	d, err := seed.Open("../../../../../testdata/seed/small")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, want := range []seed.LoadStats{{Loaded: len(d.Notifications)}, {AlreadyPresent: len(d.Notifications)}} {
		got, err := seed.LoadInbox(ctx, store, d)
		if err != nil || got != want {
			t.Fatalf("load %d: %+v, %v", i+1, got, err)
		}
	}
	var unread int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE read_at IS NULL`).Scan(&unread); err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, n := range d.Notifications {
		if !n.Read {
			want++
		}
	}
	if unread != want {
		t.Fatalf("%d unread rows, want %d", unread, want)
	}
}

// TestPostgresStoreMatchesTheFakeOnAProducerScenario runs the same events
// and inbox calls on both stores and compares every answer.
func TestPostgresStoreMatchesTheFakeOnAProducerScenario(t *testing.T) {
	pg, _ := startPostgres(t)
	ctx := context.Background()
	ws, note := newID(), newID()
	users := []string{newID(), newID(), newID()}
	run := func(store app.Store) ([]string, error) {
		ids := fakes.IDs(42)
		producer, err := app.NewProducer(app.ProducerConfig{Store: store, Authorizer: fakes.AllowAll(),
			Directory: fakes.NewDirectory(
				fakes.DirectoryUser{Profile: app.Profile{UserID: users[0], DisplayName: "Ada"}, Handle: "ada", Workspaces: []string{ws}},
				fakes.DirectoryUser{Profile: app.Profile{UserID: users[1], DisplayName: "Grace"}, Handle: "grace", Workspaces: []string{ws}}),
			UnreadCache: fakes.NewUnreadCache(), Fanout: (&fakes.FanoutBus{}).Replica(), NewID: ids,
			Clock: func() time.Time { return time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC) }})
		if err != nil {
			return nil, err
		}
		inbox := app.NewInbox(store, fakes.NewUnreadCache(), nil, nil)
		var out []string
		at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
		ev := func(i int, actor string) app.Event {
			return app.Event{Consumer: "differential", ID: seed.EventID("diff", fmt.Sprint(i)), OccurredAt: at.Add(time.Duration(i) * time.Second), ActorUserID: actor}
		}
		if _, err := producer.NoteCreated(ctx, ev(0, users[2]), note, ws, "Plan"); err != nil {
			return nil, err
		}
		for i := 1; i < 40; i++ {
			text := []string{"@ada", "@grace and @ada", "nothing", "@grace"}[i%4]
			if _, err := producer.BlocksChanged(ctx, ev(i, users[i%3]), app.BlocksChangedEvent{NoteID: note,
				Blocks: []app.ChangedBlock{{ID: fmt.Sprintf("1:%d", i%5), Kind: "paragraph", Text: text}}}); err != nil {
				return nil, err
			}
			if i%7 == 0 {
				if _, err := producer.Comment(ctx, ev(100+i, users[i%3]), app.CommentEvent{CommentID: fmt.Sprint("c", i), NoteID: note,
					Mentions: []string{users[(i+1)%3]}, Body: "x"}); err != nil {
					return nil, err
				}
			}
		}
		for _, u := range users {
			token := ""
			for {
				page, err := inbox.ListInbox(ctx, u, 3, token, false)
				if err != nil {
					return nil, err
				}
				for _, n := range page.Notifications {
					out = append(out, fmt.Sprintf("%s %s %d %s %d", n.RecipientID, n.Kind, n.Occurrences, n.Location.BlockID, page.Unread))
				}
				if token = page.NextPageToken; token == "" {
					break
				}
			}
		}
		return out, nil
	}
	want, err := run(fakes.NewStore())
	if err != nil {
		t.Fatal(err)
	}
	got, err := run(pg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Postgres:\n%v\nfake:\n%v", got, want)
	}
}

// TestConcurrentSendersNeverClaimTheSameSend checks FOR UPDATE SKIP LOCKED
// in ClaimEmailSends and PendingEmails on a real database.
func TestConcurrentSendersNeverClaimTheSameSend(t *testing.T) {
	store, _ := startPostgres(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	err := store.WithTx(ctx, func(ctx context.Context, tx app.Tx) error {
		for i := range 60 {
			n := domain.Candidate{RecipientID: newID(), Kind: domain.KindMention, Title: "t",
				Source: domain.Source{Kind: domain.SourceRequest, ID: fmt.Sprint(i)}, At: now}.Build(newID(), true)
			if err := tx.InsertNotification(ctx, n); err != nil {
				return err
			}
			if err := tx.EnqueueEmail(ctx, app.EmailJob{ID: newID(), RecipientID: n.RecipientID, NotificationID: n.ID, CreatedAt: now, DueAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mailer := &fakes.Mailer{}
			renderer, _ := newRenderer()
			sender, _ := app.NewEmailSender(app.EmailSenderConfig{Store: store, Directory: anyone{}, Mailer: mailer, Renderer: renderer,
				NewID: newID, ClaimBatch: 5, RecipientBatch: 5})
			for range 10 {
				if _, err := sender.RunOnce(ctx); err != nil {
					t.Error(err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			for _, m := range mailer.Sent() {
				claimed[m.MessageID]++
			}
		}()
	}
	wg.Wait()
	if len(claimed) != 60 {
		t.Fatalf("%d emails sent, want 60", len(claimed))
	}
	for id, n := range claimed {
		if n != 1 {
			t.Fatalf("send %s went out %d times", id, n)
		}
	}
}
