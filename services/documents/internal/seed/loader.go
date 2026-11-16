package seed

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/services/documents/internal/app"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// Clock is the loader's clock: the service and the Policy read it, and the
// loader moves it to each record's time, so permissions and history match
// the dataset. It rests at the reference time when loading ends.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Now implements ports.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Report counts what a load did.
type Report struct {
	Notes          int
	AlreadyPresent int
	Batches        int
	Ops            int
	Refused        int
	Trashed        int
}

// Load loads the dataset through svc, whose Authorizer must be a Policy on
// clock and whose ReplicaRegistry must know replicas.jsonl (Replicas). It is
// idempotent: loading twice changes nothing.
func Load(ctx context.Context, d *Dataset, svc *app.Service, clock *Clock, logger *slog.Logger) (Report, error) {
	var rep Report
	notes, err := readJSONL[noteRecord](d, "notes.jsonl")
	if err != nil {
		return rep, err
	}
	batches, err := readJSONL[batchRecord](d, "batches.jsonl")
	if err != nil {
		return rep, err
	}
	blockRecs, err := readJSONL[blockRecord](d, "blocks.jsonl")
	if err != nil {
		return rep, err
	}
	rejected, err := readJSONL[rejectedRecord](d, "rejected_ops.jsonl")
	if err != nil {
		return rep, err
	}
	memberships, err := readJSONL[membershipRecord](d, "memberships.jsonl")
	if err != nil {
		return rep, err
	}
	byNote := map[string][]batchRecord{}
	for _, b := range batches {
		byNote[b.NoteID] = append(byNote[b.NoteID], b)
	}
	blocksByNote := map[string][]blockRecord{}
	for _, b := range blockRecs {
		blocksByNote[b.NoteID] = append(blocksByNote[b.NoteID], b)
	}
	defer clock.Set(d.ReferenceTime)

	for _, n := range notes {
		if err := loadNote(ctx, d, svc, clock, n, byNote[n.ID], blocksByNote[n.ID], &rep); err != nil {
			return rep, fmt.Errorf("note %s: %w", n.ID, err)
		}
	}
	for i, r := range rejected {
		if err := refuse(ctx, svc, clock, r); err != nil {
			return rep, fmt.Errorf("rejected batch %d: %w", i, err)
		}
		rep.Refused++
	}
	owners := map[string]string{}
	for _, m := range memberships {
		if m.Role == "owner" {
			owners[m.WorkspaceID] = m.UserID
		}
	}
	for _, n := range notes {
		if n.TrashedAt == nil {
			continue
		}
		at, err := parseTime(*n.TrashedAt)
		if err != nil {
			return rep, err
		}
		clock.Set(at)
		if err := svc.DeleteNote(ctx, domain.Subject{UserID: owners[n.WorkspaceID]}, domain.NoteID(n.ID)); err != nil {
			return rep, fmt.Errorf("trashing note %s: %w", n.ID, err)
		}
		rep.Trashed++
	}
	logger.Info("seed loaded", "notes", rep.Notes, "already_present", rep.AlreadyPresent, "batches", rep.Batches, "ops", rep.Ops, "refused", rep.Refused, "trashed", rep.Trashed)
	return rep, nil
}

func subjectOf(user string, agentRun *string) domain.Subject {
	s := domain.Subject{UserID: user}
	if agentRun != nil {
		s.AgentRunID = *agentRun
	}
	return s
}

func loadNote(ctx context.Context, d *Dataset, svc *app.Service, clock *Clock, n noteRecord, batches []batchRecord, bs []blockRecord, rep *Report) error {
	created, err := parseTime(n.CreatedAt)
	if err != nil {
		return err
	}
	updated, err := parseTime(n.UpdatedAt)
	if err != nil {
		return err
	}
	clock.Set(created)
	note := domain.Note{ID: domain.NoteID(n.ID), WorkspaceID: domain.WorkspaceID(n.WorkspaceID), Title: n.Title, CreatedBy: n.CreatedBy, CreatedAt: created, UpdatedAt: created}
	switch err := svc.ImportNote(ctx, note); {
	case errors.Is(err, domain.ErrAlreadyExists):
		rep.AlreadyPresent++
		if n.TrashedAt != nil {
			// Trashing comes last, so a trashed note was loaded in full
			// (and its text can no longer change).
			return nil
		}
	case err != nil:
		return err
	default:
		rep.Notes++
	}
	ops, err := d.readChecked(n.OpsPath, n.OpsSHA256)
	if err != nil {
		return err
	}
	last := updated
	for _, b := range batches {
		payload, err := frame(ops, b)
		if err != nil {
			return err
		}
		at, err := parseTime(b.DeliveredAt)
		if err != nil {
			return err
		}
		replica, err := parseReplica(b.ReplicaID)
		if err != nil {
			return err
		}
		clock.Set(at)
		if at.After(last) {
			last = at
		}
		if _, err := svc.ApplyOps(ctx, subjectOf(b.UserID, b.AgentRunID), note.ID, crdt.ReplicaID(replica), payload); err != nil {
			return fmt.Errorf("batch %d: %w", b.Seq, err)
		}
		rep.Batches++
		rep.Ops += opCount(payload)
	}
	want := map[crdt.ID]blocks.Attrs{}
	for _, b := range bs {
		id, attrs, err := blockAttrs(b)
		if err != nil {
			return err
		}
		want[id] = attrs
	}
	clock.Set(last)
	return svc.ApplyAttrs(ctx, domain.Subject{UserID: n.CreatedBy}, note.ID, want)
}

// frame returns one batch's bytes from a note's ops file, checking the
// length prefix and the batch's own hash.
func frame(ops []byte, b batchRecord) ([]byte, error) {
	end := b.ByteOffset + b.ByteLength
	if b.ByteOffset < 1 || end > len(ops) {
		return nil, fmt.Errorf("%w: batch %d frame out of range", ErrDataset, b.Seq)
	}
	prefix := binary.AppendUvarint(nil, uint64(b.ByteLength))
	if b.ByteOffset < len(prefix) || string(ops[b.ByteOffset-len(prefix):b.ByteOffset]) != string(prefix) {
		return nil, fmt.Errorf("%w: batch %d length prefix does not match", ErrDataset, b.Seq)
	}
	payload := ops[b.ByteOffset:end]
	if sha256Hex(payload) != b.SHA256 {
		return nil, fmt.Errorf("%w: batch %d hash mismatch", ErrDataset, b.Seq)
	}
	return payload, nil
}

func opCount(batch []byte) int {
	ops, _ := crdt.DecodeOps(batch)
	return len(ops)
}

// refuse sends a rejected batch through the permission-checked path and
// checks it is refused for the recorded reason.
func refuse(ctx context.Context, svc *app.Service, clock *Clock, r rejectedRecord) error {
	at, err := parseTime(r.DeliveredAt)
	if err != nil {
		return err
	}
	replica, err := parseReplica(r.ReplicaID)
	if err != nil {
		return err
	}
	clock.Set(at)
	_, err = svc.ApplyOps(ctx, subjectOf(r.UserID, r.AgentRunID), domain.NoteID(r.NoteID), crdt.ReplicaID(replica), r.OpsBase64)
	var want error
	switch r.Reason {
	case "access_revoked":
		want = domain.ErrNotFound
	case "insufficient_role":
		want = domain.ErrPermissionDenied
	default:
		return fmt.Errorf("%w: unknown reason %q", ErrDataset, r.Reason)
	}
	if !errors.Is(err, want) {
		return fmt.Errorf("refused with %v, want %v (%s)", err, want, r.Reason)
	}
	return nil
}

func blockAttrs(b blockRecord) (crdt.ID, blocks.Attrs, error) {
	id, err := ParseBlockID(b.BlockID)
	if err != nil {
		return id, blocks.Attrs{}, err
	}
	typ, ok := blocks.ParseType(b.Type)
	if !ok {
		return id, blocks.Attrs{}, fmt.Errorf("%w: block %s type %q", ErrDataset, b.BlockID, b.Type)
	}
	a := blocks.Attrs{Type: typ, Indent: b.Indent}
	if b.Checked != nil {
		a.Checked = *b.Checked
	}
	if b.Language != nil {
		a.Language = *b.Language
	}
	return id, a, nil
}

// ParseBlockID reads a block ID ("replica:counter").
func ParseBlockID(s string) (crdt.ID, error) {
	r, c, ok := strings.Cut(s, ":")
	replica, err1 := strconv.ParseUint(r, 10, 64)
	counter, err2 := strconv.ParseUint(c, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return crdt.ID{}, fmt.Errorf("%w: block id %q", ErrDataset, s)
	}
	return crdt.ID{Replica: crdt.ReplicaID(replica), Counter: counter}, nil
}

// Verify checks every note against truth/crdt/final.jsonl (text and
// version vector) and blocks.jsonl (block IDs, attributes and texts).
func Verify(ctx context.Context, d *Dataset, svc *app.Service) error {
	finals, err := readJSONL[finalRecord](d, "truth/crdt/final.jsonl")
	if err != nil {
		return err
	}
	blockRecs, err := readJSONL[blockRecord](d, "blocks.jsonl")
	if err != nil {
		return err
	}
	blocksByNote := map[string][]blockRecord{}
	for _, b := range blockRecs {
		blocksByNote[b.NoteID] = append(blocksByNote[b.NoteID], b)
	}
	var errs []error
	for _, f := range finals {
		if err := verifyNote(ctx, svc, f, blocksByNote[f.NoteID]); err != nil {
			errs = append(errs, fmt.Errorf("note %s: %w", f.NoteID, err))
		}
	}
	return errors.Join(errs...)
}

func verifyNote(ctx context.Context, svc *app.Service, f finalRecord, want []blockRecord) error {
	got, err := svc.Inspect(ctx, domain.NoteID(f.NoteID))
	if err != nil {
		return err
	}
	if got.Text != f.Text {
		return fmt.Errorf("text differs from the CRDT truth (%d vs %d code points)", len([]rune(got.Text)), len([]rune(f.Text)))
	}
	wantVV := crdt.VersionVector{}
	for _, e := range f.VersionVector {
		r, err := parseReplica(e.ReplicaID)
		if err != nil {
			return err
		}
		wantVV[crdt.ReplicaID(r)] = e.Counter
	}
	if !got.Version.Equal(wantVV) {
		return fmt.Errorf("version vector %v, truth %v", got.Version, wantVV)
	}
	if len(got.Blocks) != len(want) {
		return fmt.Errorf("%d blocks, blocks.jsonl has %d", len(got.Blocks), len(want))
	}
	for i, w := range want {
		id, attrs, err := blockAttrs(w)
		if err != nil {
			return err
		}
		g := got.Blocks[i]
		if g.ID != id || g.Attrs != attrs || g.Text != w.Text {
			return fmt.Errorf("block %d is %s %+v, blocks.jsonl has %s %+v", i, g.ID, g.Attrs, w.BlockID, attrs)
		}
	}
	return nil
}
