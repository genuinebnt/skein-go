// Package anchor is the hand-written anchor workspace: about sixty notes of
// engineering and personal-style writing whose facts are known, so agent
// evaluations and search tests can ask questions with exact answers.
//
// Notes are Markdown files in notes/, parsed by blocks.Parse. Front matter:
//
//	title:   note title (unique in the workspace)
//	author:  cast key of the creator (corpus.AnchorCast)
//	editors: cast keys of people who edit it later, comma-separated
//	tags:    comma-separated tags
//	created: days before the reference time the note was created
//	root:    true for the workspace's root note
//	trashed: true for a note in the trash
//	acl:     per-note overrides, "castkey=role, ..."
//
// Changing a note changes the generated data and its ground truth; the
// evals below cite notes by title and an exact snippet, and generation fails
// if a cited snippet no longer exists in exactly one block.
package anchor

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/genuinebnt/skein-go/tools/seed/internal/blocks"
)

//go:embed notes/*.md
var files embed.FS

// ACLEntry is one per-note override.
type ACLEntry struct {
	CastKey string
	Role    string
}

// Note is one parsed anchor note.
type Note struct {
	File        string
	Title       string
	Author      string
	Editors     []string
	Tags        []string
	CreatedDays int
	Root        bool
	Trashed     bool
	ACL         []ACLEntry
	Blocks      []blocks.Block
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Notes parses every anchor note, in file name order.
func Notes() ([]Note, error) {
	entries, err := files.ReadDir("notes")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var out []Note
	titles := map[string]bool{}
	for _, name := range names {
		src, err := files.ReadFile("notes/" + name)
		if err != nil {
			return nil, err
		}
		doc, err := blocks.Parse(string(src))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		n := Note{File: name, Title: doc.Get("title"), Author: doc.Get("author"), Blocks: doc.Blocks,
			Editors: splitList(doc.Get("editors")), Tags: splitList(doc.Get("tags")),
			Root: doc.Get("root") == "true", Trashed: doc.Get("trashed") == "true"}
		if n.Title == "" || n.Author == "" || len(n.Blocks) == 0 {
			return nil, fmt.Errorf("%s: title, author and content are required", name)
		}
		if titles[n.Title] {
			return nil, fmt.Errorf("%s: duplicate title %q", name, n.Title)
		}
		titles[n.Title] = true
		if n.CreatedDays, err = strconv.Atoi(doc.Get("created")); err != nil {
			return nil, fmt.Errorf("%s: created: %w", name, err)
		}
		for _, a := range splitList(doc.Get("acl")) {
			k, v, ok := strings.Cut(a, "=")
			if !ok {
				return nil, fmt.Errorf("%s: acl entry %q", name, a)
			}
			n.ACL = append(n.ACL, ACLEntry{CastKey: strings.TrimSpace(k), Role: strings.TrimSpace(v)})
		}
		out = append(out, n)
	}
	return out, nil
}

// ShareLink is a fixed share link in the anchor workspace.
type ShareLink struct {
	Title       string // note title
	Role        string
	CreatedBy   string // cast key
	CreatedDays int    // days before the reference time
	// ExpiresDays is days relative to the reference time (negative: already
	// expired); 0 means no expiry.
	ExpiresDays int
	// RevokedDays is days before the reference time; 0 means not revoked.
	RevokedDays int
}

// ShareLinks cover every link state the policy distinguishes.
var ShareLinks = []ShareLink{
	{Title: "HNSW parameters", Role: "viewer", CreatedBy: "ml", CreatedDays: 20},
	{Title: "Raft overview", Role: "commenter", CreatedBy: "admin", CreatedDays: 40, ExpiresDays: -3},
	{Title: "Release checklist v0.4", Role: "editor", CreatedBy: "admin", CreatedDays: 15, RevokedDays: 6},
	{Title: "Kyoto conference trip", Role: "viewer", CreatedBy: "storage", CreatedDays: 9, ExpiresDays: 30},
}
