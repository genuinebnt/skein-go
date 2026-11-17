// Package seed loads the search service's share of a seed dataset
// (docs/seed-data.md, "Loader contract") and provides an access directory
// that answers from the dataset's memberships and per-note roles.
//
// The loader never writes projection rows directly. It turns notes.jsonl,
// blocks.jsonl and links.jsonl into the documents events the documents
// service would publish (NoteCreated, BlocksChanged, LinksChanged,
// NoteTrashed) and hands each to the same Projector the Pub/Sub consumer
// uses, so loading exercises the production path: decoding, validation,
// idempotent apply, the change feed. Event IDs are derived from the note
// and event kind (UUIDv5-style), so loading twice is a no-op, as the
// contract requires. Roots have no event yet and are applied as a
// MutationRoot through the store (see the service README).
//
// It never reads truth/.
package seed

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// FormatVersion is the dataset format this loader reads.
const FormatVersion = 1

// ErrDataset means the dataset is unusable: wrong format, a checksum
// mismatch, a malformed record.
var ErrDataset = errors.New("seed: bad dataset")

// Manifest is the part of manifest.json the loader checks.
type Manifest struct {
	FormatVersion int               `json:"format_version"`
	Profile       string            `json:"profile"`
	ReferenceTime time.Time         `json:"reference_time"`
	Files         map[string]string `json:"-"`
}

// ReadManifest reads and parses manifest.json.
func ReadManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest: %v", ErrDataset, err)
	}
	var raw struct {
		Files []struct {
			Path   string `json:"path"`
			Sha256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest files: %v", ErrDataset, err)
	}
	m.Files = map[string]string{}
	for _, f := range raw.Files {
		m.Files[f.Path] = f.Sha256
	}
	if m.FormatVersion != FormatVersion {
		return Manifest{}, fmt.Errorf("%w: format version %d, loader reads %d", ErrDataset, m.FormatVersion, FormatVersion)
	}
	return m, nil
}

// Verify checks the sha256 of each named file against the manifest.
func (m Manifest) Verify(dir string, names ...string) error {
	for _, name := range names {
		want, ok := m.Files[name]
		if !ok {
			return fmt.Errorf("%w: manifest has no checksum for %s", ErrDataset, name)
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return fmt.Errorf("%w: %s has sha256 %s, manifest says %s", ErrDataset, name, got, want)
		}
	}
	return nil
}

// ReadJSONL decodes every line of a JSON Lines file into T.
func ReadJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for line := 1; sc.Scan(); line++ {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%w: %s line %d: %v", ErrDataset, filepath.Base(path), line, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// Note is a notes.jsonl record (the fields search uses).
type Note struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Trashed     bool   `json:"trashed"`
	IsRoot      bool   `json:"is_root"`
}

// Block is a blocks.jsonl record.
type Block struct {
	NoteID   string  `json:"note_id"`
	BlockID  string  `json:"block_id"`
	Position int     `json:"position"`
	Type     string  `json:"type"`
	Language *string `json:"language"`
	Text     string  `json:"text"`
}

// Link is a links.jsonl record: one [[link]] interval.
type Link struct {
	SourceNoteID string  `json:"source_note_id"`
	TargetTitle  string  `json:"target_title"`
	TargetNoteID *string `json:"target_note_id"`
	RemovedAt    *string `json:"removed_at"`
}

// Stats summarizes a load.
type Stats struct {
	Notes, Blocks, Links int
	Events, Applied      int
}

// eventNamespace scopes the derived event IDs.
var eventNamespace = uuid.MustParse("6f4a3b9e-2c1d-5e8f-9a0b-1c2d3e4f5a6b")

func eventID(note, kind string) string {
	return uuid.NewSHA1(eventNamespace, []byte(note+"/"+kind)).String()
}

// Load reads dir's notes, blocks and links and feeds them through
// projector as documents events. Root flags go through store.
func Load(ctx context.Context, dir string, projector *app.Projector, store app.Store) (Stats, error) {
	manifest, err := ReadManifest(dir)
	if err != nil {
		return Stats{}, err
	}
	if err := manifest.Verify(dir, "notes.jsonl", "blocks.jsonl", "links.jsonl"); err != nil {
		return Stats{}, err
	}
	notes, err := ReadJSONL[Note](filepath.Join(dir, "notes.jsonl"))
	if err != nil {
		return Stats{}, err
	}
	blockRecords, err := ReadJSONL[Block](filepath.Join(dir, "blocks.jsonl"))
	if err != nil {
		return Stats{}, err
	}
	linkRecords, err := ReadJSONL[Link](filepath.Join(dir, "links.jsonl"))
	if err != nil {
		return Stats{}, err
	}
	byNote := map[string][]Block{}
	for _, b := range blockRecords {
		byNote[b.NoteID] = append(byNote[b.NoteID], b)
	}
	targets := map[string][]string{}
	// LinksChanged carries link titles, as documents publishes them; the
	// projection resolves them (dangling ones included, which resolve to
	// nothing).
	for _, l := range linkRecords {
		if l.RemovedAt == nil {
			targets[l.SourceNoteID] = append(targets[l.SourceNoteID], l.TargetTitle)
		}
	}
	stats := Stats{Notes: len(notes), Blocks: len(blockRecords)}
	send := func(note, kind string, e *skeinv1.Event) error {
		e.Id = eventID(note, kind)
		e.Source = "documents"
		e.OrderingKey = note
		payload, err := proto.Marshal(e)
		if err != nil {
			return err
		}
		out, err := projector.HandleDocuments(ctx, events.Message{Envelope: events.Envelope{ID: e.Id, Source: "documents", OrderingKey: note, Payload: payload}})
		if err != nil {
			return fmt.Errorf("note %s %s: %w", note, kind, err)
		}
		stats.Events++
		if out == app.OutcomeApplied {
			stats.Applied++
		}
		return nil
	}
	for _, n := range notes {
		if err := send(n.ID, "created", &skeinv1.Event{Payload: &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{
			NoteId: n.ID, WorkspaceId: n.WorkspaceID, Title: n.Title}}}); err != nil {
			return stats, err
		}
		bs := byNote[n.ID]
		slices.SortFunc(bs, func(a, b Block) int { return a.Position - b.Position })
		bc := &skeinv1.BlocksChanged{NoteId: n.ID}
		for _, b := range bs {
			lang := ""
			if b.Language != nil {
				lang = *b.Language
			}
			bc.Blocks = append(bc.Blocks, &skeinv1.ChangedBlock{BlockId: b.BlockID, Kind: b.Type, Language: lang, Text: b.Text})
		}
		if err := send(n.ID, "blocks", &skeinv1.Event{Payload: &skeinv1.Event_BlocksChanged{BlocksChanged: bc}}); err != nil {
			return stats, err
		}
		stats.Links += len(targets[n.ID])
		if err := send(n.ID, "links", &skeinv1.Event{Payload: &skeinv1.Event_LinksChanged{LinksChanged: &skeinv1.LinksChanged{
			NoteId: n.ID, Targets: targets[n.ID]}}}); err != nil {
			return stats, err
		}
		if n.Trashed {
			if err := send(n.ID, "trashed", &skeinv1.Event{Payload: &skeinv1.Event_NoteTrashed{NoteTrashed: &skeinv1.NoteTrashed{NoteId: n.ID}}}); err != nil {
				return stats, err
			}
		}
		if n.IsRoot {
			m := &domain.Mutation{Kind: domain.MutationRoot, NoteID: n.ID, Root: true}
			if _, err := store.Apply(ctx, app.DocumentsConsumer, eventID(n.ID, "root"), m); err != nil {
				return stats, fmt.Errorf("note %s root: %w", n.ID, err)
			}
		}
	}
	return stats, nil
}
