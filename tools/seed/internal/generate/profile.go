package generate

import (
	"fmt"
	"time"
)

// Version is the generator version written to the manifest. Bump it whenever
// a change alters the output for an existing (profile, seed), so consumers
// can tell regenerated data from stale data.
const Version = "1.0.0"

// FormatVersion is the version of the file layout and record shapes
// (testdata/seed/schema). Bump it on any incompatible change.
const FormatVersion = 1

// ReferenceTime is "now" for every profile: data is spread over the months
// before it, and every time-dependent check (share link expiry, removed
// members) is evaluated at it.
var ReferenceTime = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// DataStart is the earliest timestamp in generated data.
var DataStart = time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)

// Profile sets the size and shape of a generated dataset.
type Profile struct {
	Name string
	// Workspaces, Users and Notes are exact counts. The anchor workspace and
	// its notes are included in them.
	Workspaces int
	Users      int
	Notes      int
	// Blocks is a target; the generated count is within BlockTolerance of
	// it.
	Blocks int
	// SessionsMin and SessionsMax bound the edit sessions per procedural
	// note (the creating session included).
	SessionsMin, SessionsMax int
	// EmbedEveryNth exports block embeddings for every Nth note (1 = all).
	EmbedEveryNth int
	// Chats is the number of agent chat transcripts.
	Chats int
	// CommentsPerNote is the mean number of comment threads per note.
	CommentsPerNote float64
	// MatrixSubjects and MatrixNotes bound the permission matrix per
	// workspace.
	MatrixSubjects, MatrixNotes int
	// BlastSources and PathPairs are the number of blast-radius sources and
	// shortest-path pairs recorded per workspace.
	BlastSources, PathPairs int
	// MaxSentences caps the sentences in a generated paragraph. One op is
	// one character in wire format v1, so this is the main lever on the
	// size of the ops files.
	MaxSentences int
	// Stress replaces the procedural workspaces with the pathological ones.
	Stress bool
}

// BlockTolerance is the allowed relative difference between Profile.Blocks
// and the generated block count.
const BlockTolerance = 0.15

// Profiles are the named profiles, in size order.
var Profiles = []Profile{
	{Name: "small", Workspaces: 3, Users: 25, Notes: 300, Blocks: 4000, SessionsMin: 3, SessionsMax: 7,
		EmbedEveryNth: 1, Chats: 40, CommentsPerNote: 0.8, MatrixSubjects: 30, MatrixNotes: 25, BlastSources: 6, PathPairs: 8, MaxSentences: 4},
	{Name: "medium", Workspaces: 10, Users: 200, Notes: 8000, Blocks: 150000, SessionsMin: 3, SessionsMax: 6,
		EmbedEveryNth: 1, Chats: 300, CommentsPerNote: 0.6, MatrixSubjects: 24, MatrixNotes: 20, BlastSources: 6, PathPairs: 8, MaxSentences: 4},
	{Name: "large", Workspaces: 50, Users: 2000, Notes: 100000, Blocks: 2000000, SessionsMin: 2, SessionsMax: 4,
		EmbedEveryNth: 5, Chats: 2000, CommentsPerNote: 0.4, MatrixSubjects: 24, MatrixNotes: 20, BlastSources: 4, PathPairs: 6, MaxSentences: 2},
	{Name: "stress", Workspaces: 4, Users: 120, Notes: 150068, Blocks: 151000, SessionsMin: 1, SessionsMax: 2,
		EmbedEveryNth: 50, Chats: 30, CommentsPerNote: 0.01, MatrixSubjects: 20, MatrixNotes: 10, BlastSources: 4, PathPairs: 4, MaxSentences: 4, Stress: true},
}

// LookupProfile returns the named profile.
func LookupProfile(name string) (Profile, error) {
	for _, p := range Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("unknown profile %q (want small, medium, large or stress)", name)
}
