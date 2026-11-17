package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// Config tunes the service. Zero values take the defaults in WithDefaults.
type Config struct {
	// Tick is how often each actor runs its periodic work.
	Tick time.Duration
	// Debounce is how long BlocksChanged and LinksChanged wait after the
	// first unpublished change, so a burst of typing is one event.
	Debounce time.Duration
	// IdleTimeout evicts an actor with no sessions after this long unused.
	IdleTimeout time.Duration
	// SnapshotEvery checkpoints a note after this many new operations.
	SnapshotEvery int
	// VersionWindow groups batches into one version in ListVersions.
	VersionWindow time.Duration
	// UndoWindow groups a replica's edits into one undo group.
	UndoWindow time.Duration
	// PresenceTTL is how long a cursor lives without an update.
	PresenceTTL time.Duration
	// RecheckEvery re-checks read access of open sessions, which catches
	// revocations whose event reached another instance.
	RecheckEvery time.Duration
	// TrashRetention purges trashed notes after this long.
	TrashRetention time.Duration
	// MaxPending bounds out-of-order operations held per note.
	MaxPending int
	// SessionBuffer is the frames queued per sync client before it is cut
	// off as too slow.
	SessionBuffer int
	// MaxFrameOps bounds the operations in one catch-up frame, keeping
	// frames under the gateway's 1 MiB WebSocket limit.
	MaxFrameOps int
	// MaxBatchOps bounds the operations accepted in one batch.
	MaxBatchOps int
}

// WithDefaults fills zero fields.
func (c Config) WithDefaults() Config {
	def := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	defInt := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	def(&c.Tick, 500*time.Millisecond)
	def(&c.Debounce, 2*time.Second)
	def(&c.IdleTimeout, 5*time.Minute)
	def(&c.VersionWindow, 5*time.Minute)
	def(&c.UndoWindow, 2*time.Second)
	def(&c.PresenceTTL, 30*time.Second)
	def(&c.RecheckEvery, 30*time.Second)
	def(&c.TrashRetention, 30*24*time.Hour)
	defInt(&c.SnapshotEvery, 5000)
	defInt(&c.MaxPending, crdt.DefaultMaxPending)
	defInt(&c.SessionBuffer, 256)
	defInt(&c.MaxFrameOps, 20_000)
	defInt(&c.MaxBatchOps, 100_000)
	return c
}

// Deps are the ports the service uses.
type Deps struct {
	Store      Store
	Authorizer Authorizer
	AuthCache  AuthCache // optional
	Replicas   ReplicaRegistry
	// Notes registers created notes with identity; nil skips it (the seed
	// loader, whose notes identity's own loader projects).
	Notes     NoteRegistry
	NoteCache NoteCache
	Presence  PresenceStore
	Clock     Clock
	Logger    *slog.Logger
	// MeterProvider defaults to the OpenTelemetry global.
	MeterProvider metric.MeterProvider
}

// Service implements the DocumentService use cases. The gRPC adapter maps
// requests and errors; everything that decides lives here or in domain.
type Service struct {
	store      Store
	authorizer Authorizer
	authCache  AuthCache
	replicas   ReplicaRegistry
	registry   NoteRegistry
	notes      NoteCache
	presence   PresenceStore
	clock      Clock
	logger     *slog.Logger
	cfg        Config
	reg        *registry
	m          *metrics

	// owners caches replicas confirmed as the user's. Ownership never
	// changes once registered, so entries never go stale.
	ownersMu sync.Mutex
	owners   map[crdt.ReplicaID]string
}

// New returns a service. Call Run to start its background work.
func New(d Deps, cfg Config) *Service {
	if d.Clock == nil {
		d.Clock = SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		store: d.Store, authorizer: d.Authorizer, authCache: d.AuthCache, replicas: d.Replicas, registry: d.Notes,
		notes: d.NoteCache, presence: d.Presence, clock: d.Clock, logger: d.Logger,
		cfg: cfg.WithDefaults(), owners: map[crdt.ReplicaID]string{}, m: newMetrics(d.MeterProvider),
	}
	s.reg = newRegistry(s)
	return s
}

// getNote reads note metadata through the cache.
func (s *Service) getNote(ctx context.Context, id domain.NoteID) (domain.Note, error) {
	if n, ok := s.notes.Get(ctx, id); ok {
		return n, nil
	}
	n, err := s.store.GetNote(ctx, id)
	if err != nil {
		return n, err
	}
	s.notes.Set(ctx, n)
	return n, nil
}

// checkNote asks identity whether subject may read the note and do action
// to it. A note the subject cannot read, or that does not exist, is
// ErrNotFound, so IDs cannot be probed; one it can read but not act on is
// ErrPermissionDenied.
func (s *Service) checkNote(ctx context.Context, subject domain.Subject, id domain.NoteID, action Action) error {
	if subject.UserID == "" {
		return fmt.Errorf("%w: no caller", domain.ErrPermissionDenied)
	}
	checks := []Check{{Action: ActionRead, Resource: string(id)}}
	if action != ActionRead {
		checks = append(checks, Check{Action: action, Resource: string(id)})
	}
	allowed, err := s.authorizer.Authorize(ctx, subject, checks)
	if err != nil {
		return fmt.Errorf("%w: authorize: %v", domain.ErrUnavailable, err)
	}
	if len(allowed) != len(checks) {
		return fmt.Errorf("%w: authorize answered %d of %d checks", domain.ErrUnavailable, len(allowed), len(checks))
	}
	if !allowed[0] {
		return fmt.Errorf("%w: note %s", domain.ErrNotFound, id)
	}
	if action != ActionRead && !allowed[1] {
		return fmt.Errorf("%w: may not do this to note %s", domain.ErrPermissionDenied, id)
	}
	return nil
}

// access checks permission and returns the note's metadata.
func (s *Service) access(ctx context.Context, subject domain.Subject, id domain.NoteID, action Action) (domain.Note, error) {
	if err := s.checkNote(ctx, subject, id, action); err != nil {
		return domain.Note{}, err
	}
	return s.getNote(ctx, id)
}

// ensureReplica checks that a client replica belongs to the subject.
func (s *Service) ensureReplica(ctx context.Context, subject domain.Subject, replica crdt.ReplicaID) error {
	if replica == 0 || replica == domain.ServerReplica {
		return fmt.Errorf("%w: replica %d is reserved", domain.ErrInvalidArgument, replica)
	}
	s.ownersMu.Lock()
	owner, known := s.owners[replica]
	s.ownersMu.Unlock()
	if known {
		if owner != subject.UserID {
			return fmt.Errorf("%w: replica %d belongs to another user", domain.ErrPermissionDenied, replica)
		}
		return nil
	}
	if err := s.replicas.EnsureReplica(ctx, subject, replica); err != nil {
		return err
	}
	s.ownersMu.Lock()
	s.owners[replica] = subject.UserID
	s.ownersMu.Unlock()
	return nil
}

// CreateNote creates a note, with optional initial Markdown, and publishes
// NoteCreated and the initial block and link events in the same transaction.
func (s *Service) CreateNote(ctx context.Context, subject domain.Subject, workspace domain.WorkspaceID, title, markdown string) (domain.Note, error) {
	title, err := domain.NormalizeTitle(title)
	if err != nil {
		return domain.Note{}, err
	}
	allowed, err := s.authorizer.Authorize(ctx, subject, []Check{{Action: ActionCreate, Resource: string(workspace)}})
	if err != nil {
		return domain.Note{}, fmt.Errorf("%w: authorize: %v", domain.ErrUnavailable, err)
	}
	if len(allowed) != 1 || !allowed[0] {
		return domain.Note{}, fmt.Errorf("%w: may not create notes in this workspace", domain.ErrPermissionDenied)
	}
	id, err := domain.NewNoteID()
	if err != nil {
		return domain.Note{}, err
	}
	now := s.clock.Now()
	note := domain.Note{ID: id, WorkspaceID: workspace, Title: title, CreatedBy: subject.UserID, CreatedAt: now, UpdatedAt: now}
	if err := s.insertNote(ctx, subject, note, blocks.Import(markdown)); err != nil {
		return domain.Note{}, err
	}
	s.registerNote(ctx, subject, note)
	return note, nil
}

// registerNote tells identity about a committed note at once. A failure is
// logged, not returned: the note exists, and the NoteCreated event brings
// identity up to date a moment later.
func (s *Service) registerNote(ctx context.Context, subject domain.Subject, note domain.Note) {
	if s.registry == nil {
		return
	}
	if err := s.registry.RegisterNote(ctx, subject, note.ID, note.WorkspaceID); err != nil {
		s.logger.WarnContext(ctx, "registering the new note with identity failed; it becomes usable when NoteCreated arrives",
			"note_id", string(note.ID), "error", err)
	}
}

// ImportNote creates a note with the given identity and no text, keeping
// the dataset's IDs (the seed loader). ErrAlreadyExists if it exists.
func (s *Service) ImportNote(ctx context.Context, note domain.Note) error {
	title, err := domain.NormalizeTitle(note.Title)
	if err != nil {
		return err
	}
	note.Title = title
	return s.insertNote(ctx, domain.Subject{UserID: note.CreatedBy}, note, nil)
}

func (s *Service) insertNote(ctx context.Context, subject domain.Subject, note domain.Note, content []blocks.Block) error {
	events := []domain.Event{{Kind: domain.EventNoteCreated, NoteID: note.ID, WorkspaceID: note.WorkspaceID, Title: note.Title, Actor: subject, At: note.CreatedAt}}
	var first *domain.Batch
	var bs []blocks.Block
	if len(content) > 0 {
		doc, err := blocks.NewDocument(domain.ServerReplica)
		if err != nil {
			return err
		}
		ops, writes, err := doc.AppendBlocks(content)
		if err != nil {
			return fmt.Errorf("%w: %v", domain.ErrInvalidArgument, err)
		}
		first = &domain.Batch{
			Seq: 1, Replica: domain.ServerReplica, First: 1, Last: uint64(len(ops)), Kind: domain.KindImport,
			UserID: subject.UserID, AgentRunID: subject.AgentRunID, At: note.CreatedAt, Ops: ops, Attrs: writes,
		}
		if bs, err = doc.Blocks(); err != nil {
			return err
		}
	}
	seq := int64(0)
	if first != nil {
		seq = 1
	}
	projection, more := domain.NextProjection(note, domain.Projection{}, bs, seq, subject, note.CreatedAt)
	return s.store.InsertNote(ctx, note, first, projection, append(events, more...))
}

// GetNote returns a note's metadata and, if asked, its Markdown and the
// version it reflects.
func (s *Service) GetNote(ctx context.Context, subject domain.Subject, id domain.NoteID, withMarkdown bool) (domain.Note, string, crdt.VersionVector, error) {
	note, err := s.access(ctx, subject, id, ActionRead)
	if err != nil || !withMarkdown {
		return note, "", nil, err
	}
	var md string
	var vv crdt.VersionVector
	err = s.reg.do(ctx, id, func(st *noteState) error {
		bs, err := st.blocks()
		if err != nil {
			return err
		}
		md, vv, note = blocks.Export(bs), st.doc.Sequence().Version(), st.note
		st.lastUsed = s.clock.Now()
		return nil
	})
	return note, md, vv, err
}

// Default and maximum page sizes (docs/api/README.md).
const (
	defaultPageSize = 50
	maxPageSize     = 500
)

func pageSize(n uint32) int {
	switch {
	case n == 0:
		return defaultPageSize
	case n > maxPageSize:
		return maxPageSize
	}
	return int(n)
}

// pageToken is an opaque cursor: the last item returned and a fingerprint
// of the query, so a token reused with other parameters is refused.
func pageToken(after, fingerprint string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(after + "|" + fingerprint))
}

func parsePageToken(token, fingerprint string) (string, error) {
	if token == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	after, fp, ok := strings.Cut(string(raw), "|")
	if err != nil || !ok || fp != fingerprint {
		return "", fmt.Errorf("%w: page token does not belong to this query", domain.ErrInvalidArgument)
	}
	return after, nil
}

func fingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:6])
}

// ListNotes lists the notes of a workspace the caller can read, by ID.
func (s *Service) ListNotes(ctx context.Context, subject domain.Subject, workspace domain.WorkspaceID, size uint32, token string, includeTrashed bool) ([]domain.Note, string, error) {
	fp := fingerprint("notes", string(workspace), subject.UserID, fmt.Sprint(includeTrashed))
	after, err := parsePageToken(token, fp)
	if err != nil {
		return nil, "", err
	}
	readable, err := s.authorizer.ReadableNotes(ctx, subject, workspace)
	if err != nil {
		return nil, "", fmt.Errorf("%w: readable notes: %v", domain.ErrUnavailable, err)
	}
	limit := pageSize(size)
	notes, err := s.store.ListNotes(ctx, NoteQuery{Workspace: workspace, IDs: readable, After: domain.NoteID(after), Limit: limit + 1, IncludeTrashed: includeTrashed})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(notes) > limit {
		notes = notes[:limit]
		next = pageToken(string(notes[limit-1].ID), fp)
	}
	return notes, next, nil
}

// updateNote applies a metadata change under the note's actor when one is
// running (so the actor's copy stays current), stores it with its event and
// drops the cache entry.
func (s *Service) updateNote(ctx context.Context, subject domain.Subject, id domain.NoteID, action Action, change func(*domain.Note, time.Time) (domain.EventKind, bool, error)) error {
	if _, err := s.access(ctx, subject, id, action); err != nil {
		return err
	}
	return s.reg.do(ctx, id, func(st *noteState) error {
		note := st.note
		now := s.clock.Now()
		kind, changed, err := change(&note, now)
		if err != nil || !changed {
			return err
		}
		ev := domain.Event{Kind: kind, NoteID: id, WorkspaceID: note.WorkspaceID, Title: note.Title, Actor: subject, At: now}
		if kind != domain.EventNoteRenamed {
			ev.Title = ""
		}
		if err := s.store.UpdateNote(ctx, note, []domain.Event{ev}); err != nil {
			return err
		}
		st.note = note
		s.notes.Delete(ctx, id)
		if note.Trashed() {
			st.closeSessions(fmt.Errorf("%w: the note was moved to the trash", domain.ErrFailedPrecondition))
		}
		return nil
	})
}

// RenameNote sets a note's title.
func (s *Service) RenameNote(ctx context.Context, subject domain.Subject, id domain.NoteID, title string) error {
	return s.updateNote(ctx, subject, id, ActionEdit, func(n *domain.Note, now time.Time) (domain.EventKind, bool, error) {
		changed, err := n.Rename(title, now)
		return domain.EventNoteRenamed, changed, err
	})
}

// DeleteNote moves a note to the trash.
func (s *Service) DeleteNote(ctx context.Context, subject domain.Subject, id domain.NoteID) error {
	return s.updateNote(ctx, subject, id, ActionDelete, func(n *domain.Note, now time.Time) (domain.EventKind, bool, error) {
		return domain.EventNoteTrashed, n.Trash(now), nil
	})
}

// RestoreNote takes a note out of the trash.
func (s *Service) RestoreNote(ctx context.Context, subject domain.Subject, id domain.NoteID) error {
	return s.updateNote(ctx, subject, id, ActionDelete, func(n *domain.Note, now time.Time) (domain.EventKind, bool, error) {
		return domain.EventNoteRestored, n.Restore(now), nil
	})
}

// ApplyOps applies a batch from a client that is not streaming (the agent,
// imports, scripts). The whole batch is refused when the caller may not edit
// or the replica is not theirs; operations the CRDT refuses individually
// (reused IDs) are reported as an error after the rest were applied, since
// each operation is idempotent and a retry is safe.
func (s *Service) ApplyOps(ctx context.Context, subject domain.Subject, id domain.NoteID, replica crdt.ReplicaID, data []byte) (crdt.VersionVector, error) {
	ops, err := crdt.DecodeOps(data)
	if err != nil {
		return nil, fmt.Errorf("%w: ops: %v", domain.ErrInvalidArgument, err)
	}
	if len(ops) > s.cfg.MaxBatchOps {
		return nil, fmt.Errorf("%w: batch of %d operations exceeds %d", domain.ErrInvalidArgument, len(ops), s.cfg.MaxBatchOps)
	}
	for _, op := range ops {
		if op.ID.Replica != replica {
			return nil, fmt.Errorf("%w: operation %s is not from replica %d", domain.ErrInvalidArgument, op.ID, replica)
		}
	}
	if err := s.checkNote(ctx, subject, id, ActionEdit); err != nil {
		return nil, err
	}
	if err := s.ensureReplica(ctx, subject, replica); err != nil {
		return nil, err
	}
	kind := domain.KindEdit
	if subject.IsAgent() {
		kind = domain.KindSuggestion
	}
	sender := domain.Sender{Replica: replica, UserID: subject.UserID, AgentRunID: subject.AgentRunID, Kind: kind}
	var vv crdt.VersionVector
	var res deliverResult
	err = s.reg.do(ctx, id, func(st *noteState) error {
		var err error
		res, err = st.deliver(ctx, sender, subject, ops)
		vv = st.doc.Sequence().Version()
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(res.Rejected) > 0 {
		s.m.reject(ctx, "crdt")
		return vv, fmt.Errorf("%w: %d operations refused: %s", domain.ErrInvalidArgument, len(res.Rejected), res.Reason)
	}
	return vv, nil
}

// ApplyAttrs sets block attributes with writes stamped by the server. It is
// for trusted callers only (the seed loader, which carries attributes in
// blocks.jsonl): no RPC can send attribute writes until the wire format
// carries them, so it checks no permission. Writes that change nothing are
// not stored, which keeps reloading idempotent.
func (s *Service) ApplyAttrs(ctx context.Context, subject domain.Subject, id domain.NoteID, want map[crdt.ID]blocks.Attrs) error {
	return s.reg.do(ctx, id, func(st *noteState) error {
		if err := st.note.CheckEditable(); err != nil {
			return err
		}
		ids := make([]crdt.ID, 0, len(want))
		for block := range want {
			ids = append(ids, block)
		}
		slices.SortFunc(ids, func(a, b crdt.ID) int { return a.Compare(b) })
		// Stamped with the sequence clock, so the attributes are visible at
		// every version that includes the last operation (spec section 3).
		lamport := max(st.doc.Sequence().Lamport(), 1)
		var writes []blocks.AttrWrite
		for _, block := range ids {
			for _, w := range st.doc.Attributes().Diff(block, want[block], lamport, domain.ServerReplica) {
				if _, err := st.doc.Attributes().Apply(w); err != nil {
					return fmt.Errorf("%w: %v", domain.ErrInvalidArgument, err)
				}
				writes = append(writes, w)
			}
		}
		if len(writes) == 0 {
			return nil
		}
		now := s.clock.Now()
		batch := domain.Batch{Seq: st.lastSeq() + 1, Kind: domain.KindAttrs, UserID: subject.UserID, At: now, Attrs: writes}
		return st.commit(ctx, []domain.Batch{batch}, subject, now)
	})
}

// Inspection is a note's raw state, for loaders and diagnostics.
type Inspection struct {
	Note    domain.Note
	Text    string // the sequence text, markers included
	Version crdt.VersionVector
	Blocks  []blocks.Block
}

// Inspect returns a note's raw state without a permission check; only
// trusted callers (the seed loader's verification) use it.
func (s *Service) Inspect(ctx context.Context, id domain.NoteID) (Inspection, error) {
	var out Inspection
	err := s.reg.do(ctx, id, func(st *noteState) error {
		bs, err := st.blocks()
		out = Inspection{Note: st.note, Text: st.doc.Sequence().Text(), Version: st.doc.Sequence().Version(), Blocks: bs}
		return err
	})
	return out, err
}
