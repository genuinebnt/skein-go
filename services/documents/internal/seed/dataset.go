// Package seed loads the documents service's share of a seed dataset
// (docs/seed-data.md, "Loader contract"): notes, then each note's ops file
// batch by batch through the same permission-checked ApplyOps path live
// clients use, then block attributes from blocks.jsonl (which the wire
// format cannot carry yet), then every rejected batch, which must be refused
// for the recorded reason, then trashing. Verify compares the loaded notes
// with truth/crdt/final.jsonl; Load never reads truth/.
//
// Identity answers Authorize only for the present, but each batch must be
// checked as of its delivery, so the loader decides permissions with Policy,
// the documented policy evaluated over the dataset's memberships and
// overrides at the loader's clock. Replica ownership comes from
// replicas.jsonl.
//
// Comments are documents' data too, but no RPC or contract for them exists
// yet; they are left for the comments milestone.
package seed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ErrDataset means the dataset is missing, unsupported or does not match
// its manifest.
var ErrDataset = errors.New("seed: bad dataset")

// FormatVersion is the dataset format this loader reads.
const FormatVersion = 1

// Dataset is an opened, verified seed directory.
type Dataset struct {
	Dir           string
	ReferenceTime time.Time
	sha           map[string]string
}

type manifest struct {
	FormatVersion int    `json:"format_version"`
	ReferenceTime string `json:"reference_time"`
	BlockMarker   string `json:"block_marker"`
	Files         []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// Open reads and checks a dataset's manifest.
func Open(dir string) (*Dataset, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDataset, err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", ErrDataset, err)
	}
	if m.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("%w: format version %d, this loader reads %d", ErrDataset, m.FormatVersion, FormatVersion)
	}
	if m.BlockMarker != "\u001e" {
		return nil, fmt.Errorf("%w: block marker %q", ErrDataset, m.BlockMarker)
	}
	ref, err := time.Parse(time.RFC3339Nano, m.ReferenceTime)
	if err != nil {
		return nil, fmt.Errorf("%w: reference time: %v", ErrDataset, err)
	}
	d := &Dataset{Dir: dir, ReferenceTime: ref, sha: map[string]string{}}
	for _, f := range m.Files {
		d.sha[f.Path] = f.SHA256
	}
	return d, nil
}

// readFile returns a file's bytes after checking them against the manifest.
func (d *Dataset) readFile(path string) ([]byte, error) {
	want, ok := d.sha[path]
	if !ok {
		return nil, fmt.Errorf("%w: %s is not in the manifest", ErrDataset, path)
	}
	return d.readChecked(path, want)
}

func (d *Dataset) readChecked(path, want string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(d.Dir, filepath.FromSlash(path)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDataset, err)
	}
	if got := sha256Hex(data); got != want {
		return nil, fmt.Errorf("%w: %s has sha256 %s, manifest says %s", ErrDataset, path, got, want)
	}
	return data, nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// readJSONL decodes every line of a verified JSONL file.
func readJSONL[T any](d *Dataset, path string) ([]T, error) {
	data, err := d.readFile(path)
	if err != nil {
		return nil, err
	}
	return decodeJSONL[T](data, path)
}

func decodeJSONL[T any](data []byte, path string) ([]T, error) {
	var out []T
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var v T
		if err := dec.Decode(&v); err == io.EOF {
			return out, nil
		} else if err != nil {
			return nil, fmt.Errorf("%w: %s record %d: %v", ErrDataset, path, len(out)+1, err)
		}
		out = append(out, v)
	}
}

// Records of the files the loader reads (testdata/seed/schema).

type noteRecord struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Title       string  `json:"title"`
	CreatedBy   string  `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	Trashed     bool    `json:"trashed"`
	TrashedAt   *string `json:"trashed_at"`
	OpsPath     string  `json:"ops_path"`
	OpsSHA256   string  `json:"ops_sha256"`
}

type batchRecord struct {
	NoteID      string  `json:"note_id"`
	Seq         int     `json:"seq"`
	ReplicaID   string  `json:"replica_id"`
	UserID      string  `json:"user_id"`
	AgentRunID  *string `json:"agent_run_id"`
	DeliveredAt string  `json:"delivered_at"`
	ByteOffset  int     `json:"byte_offset"`
	ByteLength  int     `json:"byte_length"`
	SHA256      string  `json:"sha256"`
}

type rejectedRecord struct {
	NoteID      string  `json:"note_id"`
	ReplicaID   string  `json:"replica_id"`
	UserID      string  `json:"user_id"`
	AgentRunID  *string `json:"agent_run_id"`
	DeliveredAt string  `json:"delivered_at"`
	Reason      string  `json:"reason"`
	OpsBase64   []byte  `json:"ops_base64"`
}

type blockRecord struct {
	NoteID   string  `json:"note_id"`
	BlockID  string  `json:"block_id"`
	Position int     `json:"position"`
	Type     string  `json:"type"`
	Indent   uint32  `json:"indent"`
	Checked  *bool   `json:"checked"`
	Language *string `json:"language"`
	Text     string  `json:"text"`
}

type replicaRecord struct {
	ReplicaID string `json:"replica_id"`
	UserID    string `json:"user_id"`
}

type membershipRecord struct {
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	Role        string  `json:"role"`
	JoinedAt    string  `json:"joined_at"`
	RemovedAt   *string `json:"removed_at"`
}

type aclRecord struct {
	NoteID    string `json:"note_id"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

type finalRecord struct {
	NoteID        string `json:"note_id"`
	Text          string `json:"text"`
	VersionVector []struct {
		ReplicaID string `json:"replica_id"`
		Counter   uint64 `json:"counter"`
	} `json:"version_vector"`
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return t, fmt.Errorf("%w: time %q: %v", ErrDataset, s, err)
	}
	return t.UTC(), nil
}

func parseOptionalTime(s *string) (time.Time, error) {
	if s == nil {
		return time.Time{}, nil
	}
	return parseTime(*s)
}

func parseReplica(s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: replica %q", ErrDataset, s)
	}
	return v, nil
}
