package seed_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/redisx"

	redisadapter "github.com/genuinebnt/skein-go/services/identity/internal/adapters/redis"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
	"github.com/genuinebnt/skein-go/services/identity/internal/fakes"
	"github.com/genuinebnt/skein-go/services/identity/internal/seed"
)

var (
	ctx       = context.Background()
	smallSeed = filepath.Join("..", "..", "..", "..", "testdata", "seed", "small")
)

func load(t *testing.T) (*fakes.Store, seed.Report) {
	t.Helper()
	store := fakes.NewStore()
	rep, err := seed.Load(ctx, store, seed.Options{Dir: smallSeed})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return store, rep
}

func TestLoadingTheSmallSeedLoadsIdentitysShare(t *testing.T) {
	store, rep := load(t)
	want := seed.Report{Users: 25, Workspaces: 3, Notes: 300, Memberships: 35, Removed: 3, Overrides: 25, ShareLinks: 8, Replicas: 70,
		ReferenceTime: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	if !rep.ReferenceTime.Equal(want.ReferenceTime) {
		t.Fatalf("reference time %v", rep.ReferenceTime)
	}
	rep.ReferenceTime = want.ReferenceTime
	if rep != want {
		t.Fatalf("report %+v, want %+v", rep, want)
	}
	for _, l := range store.ShareLinks() {
		if strings.HasPrefix(l.TokenHash, "shl_") || len(l.TokenHash) != 64 {
			t.Fatalf("share link %s stores %q, not a SHA-256", l.ID, l.TokenHash)
		}
	}
	if len(store.Published()) != 0 {
		t.Fatal("loading published events")
	}
}

func TestLoadingTwiceChangesNothing(t *testing.T) {
	store, _ := load(t)
	before := store.Dump()
	if _, err := seed.Load(ctx, store, seed.Options{Dir: smallSeed}); err != nil {
		t.Fatal(err)
	}
	if after := store.Dump(); after != before {
		t.Fatal("second load changed the state")
	}
}

func TestLoaderRefusesATamperedOrForeignDataset(t *testing.T) {
	copyDir := func(t *testing.T) string {
		dir := t.TempDir()
		for _, name := range append([]string{"manifest.json"}, seed.Files...) {
			data, err := os.ReadFile(filepath.Join(smallSeed, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	tampered := copyDir(t)
	acl, _ := os.ReadFile(filepath.Join(tampered, "acl.jsonl"))
	_ = os.WriteFile(filepath.Join(tampered, "acl.jsonl"), []byte(strings.Replace(string(acl), `"viewer"`, `"editor"`, 1)), 0o600)
	if _, err := seed.Load(ctx, fakes.NewStore(), seed.Options{Dir: tampered}); !errors.Is(err, seed.ErrManifest) {
		t.Fatalf("tampered acl: %v", err)
	}
	future := copyDir(t)
	m, _ := os.ReadFile(filepath.Join(future, "manifest.json"))
	_ = os.WriteFile(filepath.Join(future, "manifest.json"), []byte(strings.Replace(string(m), `"format_version": 1`, `"format_version": 2`, 1)), 0o600)
	if _, err := seed.Load(ctx, fakes.NewStore(), seed.Options{Dir: future}); !errors.Is(err, seed.ErrManifest) {
		t.Fatalf("format version 2: %v", err)
	}
}

func TestLoadedUsersCanSignInWithTheDevPassword(t *testing.T) {
	hasher, _ := credential.NewHasher(credential.HasherOptions{Params: credential.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}})
	hash, _ := hasher.Hash(ctx, "skein local dev only")
	store := fakes.NewStore()
	if _, err := seed.Load(ctx, store, seed.Options{Dir: smallSeed, PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	svc, _ := app.New(app.DefaultConfig(), app.Deps{Store: store, Hasher: hasher, Issuer: fakes.NewIssuer(time.Now)})
	if _, _, err := svc.SignIn(ctx, "mira@kestrel-lab.example", "skein local dev only", ""); err != nil {
		t.Fatal(err)
	}
}

type matrixRow struct {
	Subject struct {
		UserID      *string `json:"user_id"`
		AgentRunID  *string `json:"agent_run_id"`
		ShareLinkID *string `json:"share_link_id"`
	} `json:"subject"`
	WorkspaceID string    `json:"workspace_id"`
	Action      string    `json:"action"`
	ResourceID  string    `json:"resource_id"`
	At          time.Time `json:"at"`
	Expected    string    `json:"expected"`
	Rule        string    `json:"rule"`
}

func (r matrixRow) subject() domain.Subject {
	switch {
	case r.Subject.ShareLinkID != nil:
		return domain.LinkSubject(domain.ShareLinkID(*r.Subject.ShareLinkID))
	case r.Subject.AgentRunID != nil:
		return domain.UserSubject(domain.UserID(*r.Subject.UserID), domain.AgentRunID(*r.Subject.AgentRunID))
	}
	return domain.UserSubject(domain.UserID(*r.Subject.UserID), "")
}

func (r matrixRow) check() domain.Check {
	return domain.Check{Action: domain.ParseAction(r.Action), ResourceID: r.ResourceID}
}

func matrix(t *testing.T) []matrixRow {
	t.Helper()
	rows, err := seed.ReadJSONL[matrixRow](smallSeed, filepath.Join("truth", "permissions", "matrix.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// service returns a Service over the loaded store with the clock fixed at
// the reference time, as the loader contract requires.
func service(t *testing.T, store app.Store, at time.Time, cache app.DecisionCache) *app.Service {
	t.Helper()
	hasher, _ := credential.NewHasher(credential.HasherOptions{Params: credential.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}})
	svc, err := app.New(app.DefaultConfig(), app.Deps{Store: store, Hasher: hasher, Issuer: fakes.NewIssuer(func() time.Time { return at }),
		Cache: cache, Clock: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestAuthorizeAfterLoadingMatchesEveryMatrixRow runs every row of the
// permission matrix through the full use case (loader, repositories,
// facts loading, policy), first without a cache comparing rule strings,
// then with the decision cache comparing outcomes in two passes, so the
// second pass is answered from the cache.
func TestAuthorizeAfterLoadingMatchesEveryMatrixRow(t *testing.T) {
	store, rep := load(t)
	rows := matrix(t)
	if len(rows) != 6256 {
		t.Fatalf("%d rows", len(rows))
	}
	plain := service(t, store, rep.ReferenceTime, nil)
	cache, _ := redisadapter.NewDecisionCache(redisx.NewMemoryStore(func() time.Time { return rep.ReferenceTime }), time.Minute, nil)
	cached := service(t, store, rep.ReferenceTime, cache)
	bad := 0
	for pass := range 3 {
		for i, row := range rows {
			if !row.At.Equal(rep.ReferenceTime) {
				t.Fatalf("row %d is at %v; this test assumes the reference time", i, row.At)
			}
			svc := plain
			if pass > 0 {
				svc = cached
			}
			d, err := svc.Authorize(ctx, row.subject(), []domain.Check{row.check()})
			if err != nil {
				t.Fatal(err)
			}
			want := row.Expected == "allow"
			if d[0].Allowed != want || (pass == 0 && d[0].Rule != row.Rule) {
				bad++
				if bad < 20 {
					t.Errorf("pass %d row %d %s %s: got %+v, want %s (%s)", pass, i, row.Action, row.ResourceID, d[0], row.Expected, row.Rule)
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d disagreements", bad)
	}
}

// TestListReadableNotesAgreesWithEveryMatrixReadRow checks the list against
// each READ row for user and agent subjects: a note is listed exactly when
// the matrix allows reading it.
func TestListReadableNotesAgreesWithEveryMatrixReadRow(t *testing.T) {
	store, rep := load(t)
	svc := service(t, store, rep.ReferenceTime, nil)
	lists := map[string][]domain.NoteID{}
	checked := 0
	for _, row := range matrix(t) {
		s := row.subject()
		if row.Action != "ACTION_NOTE_READ" || s.Kind == domain.SubjectShareLink {
			continue
		}
		key := string(s.UserID) + "/" + row.WorkspaceID
		list, ok := lists[key]
		if !ok {
			var err error
			if list, err = svc.ListReadableNotes(ctx, s, domain.WorkspaceID(row.WorkspaceID)); err != nil {
				t.Fatal(err)
			}
			lists[key] = list
		}
		if listed := slices.Contains(list, domain.NoteID(row.ResourceID)); listed != (row.Expected == "allow") {
			t.Errorf("%s reading %s: listed=%v, matrix says %s (%s)", s.UserID, row.ResourceID, listed, row.Expected, row.Rule)
		}
		checked++
	}
	if checked < 1000 {
		t.Fatalf("only %d READ rows checked", checked)
	}
}
