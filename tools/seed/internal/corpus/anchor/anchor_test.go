package anchor

import (
	"strings"
	"testing"
)

func TestEveryAnchorNoteParses(t *testing.T) {
	notes, err := Notes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) < 55 || len(notes) > 70 {
		t.Fatalf("%d anchor notes, want about 60", len(notes))
	}
	roots := 0
	for _, n := range notes {
		if n.Root {
			roots++
		}
	}
	if roots != 1 {
		t.Fatalf("%d root notes, want 1", roots)
	}
}

func TestEvalCitationsNameExistingNotes(t *testing.T) {
	notes, err := Notes()
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]Note{}
	for _, n := range notes {
		byTitle[n.Title] = n
	}
	for _, e := range Evals {
		for _, c := range e.Citations {
			n, ok := byTitle[c.Title]
			if !ok {
				t.Errorf("%s: cites missing note %q", e.ID, c.Title)
				continue
			}
			hits := 0
			for _, b := range n.Blocks {
				if strings.Contains(b.Text, c.Snippet) {
					hits++
				}
			}
			if hits != 1 {
				t.Errorf("%s: snippet %q occurs in %d blocks of %q, want 1", e.ID, c.Snippet, hits, c.Title)
			}
		}
	}
}
