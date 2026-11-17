//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

// search runs a search as u and returns the response (failing on non-200).
func (u *user) search(workspace string, query url.Values) *skeinv1.SearchResponse {
	u.t.Helper()
	var resp skeinv1.SearchResponse
	u.call(http.MethodGet, "/v1/workspaces/"+workspace+"/search?"+query.Encode(), nil, &resp)
	return &resp
}

func hitIn(resp *skeinv1.SearchResponse, noteID string) *skeinv1.SearchHit {
	for _, h := range resp.GetHits() {
		if h.GetLocation().GetNoteId() == noteID {
			return h
		}
	}
	return nil
}

// codeword is a made-up word no other note contains, so a search for it has
// exactly one right answer whatever else the stack holds.
func codeword(t *testing.T) string {
	letters := "bcdfghjklmnpqrstvwxz"
	raw := randomHex(t, 6)
	var b strings.Builder
	b.WriteString("zq")
	for _, c := range raw {
		b.WriteByte(letters[int(c)%len(letters)])
	}
	return b.String()
}

func TestSearchFindsTextTypedIntoANoteOnceIndexed(t *testing.T) {
	t.Parallel()
	owner := signUp(t, "searcher")
	ws := owner.createWorkspace("Search " + randomHex(t, 3))
	note := owner.createNote(ws, "Field notes", "Nothing to see yet.")
	r := newReplica(t, owner, note.GetId()).mustConnect()
	defer r.disconnect()
	eventually(t, longWait, "the replica has the note", func() (bool, string) {
		return strings.Contains(r.text(), "Nothing to see"), r.text()
	})

	word := codeword(t)
	r.appendText(" The migration is codenamed " + word + " for now.")
	q := url.Values{"query": {word}, "mode": {"SEARCH_MODE_TEXT"}}
	eventually(t, longWait, "search finds the newly typed word", func() (bool, string) {
		resp := owner.search(ws, q)
		hit := hitIn(resp, note.GetId())
		return hit != nil && strings.Contains(hit.GetSnippet(), word), fmt.Sprintf("%d hits", len(resp.GetHits()))
	})
	hit := hitIn(owner.search(ws, q), note.GetId())
	if hit.GetLocation().GetBlockId() == "" || len(hit.GetHighlights()) == 0 {
		t.Errorf("hit lacks a block or highlights: %v", hit)
	}

	// Deleting the word takes it out of the index again.
	text := r.text()
	at := strings.Index(text, word)
	r.remove(len([]rune(text[:at])), len([]rune(word)))
	eventually(t, longWait, "search forgets the deleted word", func() (bool, string) {
		return hitIn(owner.search(ws, q), note.GetId()) == nil, "still found"
	})
}

func TestRegexCodeSearchMatchesOnlyInsideCodeBlocks(t *testing.T) {
	t.Parallel()
	owner := signUp(t, "coder")
	ws := owner.createWorkspace("Code " + randomHex(t, 3))
	ident := "Shard" + strings.ToUpper(codeword(t)[2:])
	code := owner.createNote(ws, "Shard lifecycle", fmt.Sprintf(
		"How shards close.\n\n```go\nfunc (s *%s) Close() error {\n\treturn s.file.Close()\n}\n```\n", ident))
	prose := owner.createNote(ws, "Shard prose", fmt.Sprintf("In prose: func (s *%s) Close() error is mentioned.", ident))

	q := url.Values{
		"query":    {`func \(s \*` + ident + `\) Close\(\) error`},
		"mode":     {"SEARCH_MODE_REGEX"},
		"codeOnly": {"true"},
	}
	eventually(t, longWait, "regex code search finds the code block", func() (bool, string) {
		resp := owner.search(ws, q)
		return hitIn(resp, code.GetId()) != nil, fmt.Sprintf("%d hits", len(resp.GetHits()))
	})
	resp := owner.search(ws, q)
	if hit := hitIn(resp, code.GetId()); hit.GetLanguage() != "go" {
		t.Errorf("hit language %q, want go", hit.GetLanguage())
	}
	if hitIn(resp, prose.GetId()) != nil {
		t.Error("codeOnly matched a paragraph")
	}
	// Without codeOnly the same pattern finds both.
	q.Del("codeOnly")
	eventually(t, longWait, "regex search without codeOnly finds the prose too", func() (bool, string) {
		resp := owner.search(ws, q)
		return hitIn(resp, code.GetId()) != nil && hitIn(resp, prose.GetId()) != nil, fmt.Sprintf("%d hits", len(resp.GetHits()))
	})
}

func TestTitleSuggestionsTolerateATypo(t *testing.T) {
	t.Parallel()
	owner := signUp(t, "titler")
	ws := owner.createWorkspace("Titles " + randomHex(t, 3))
	want := owner.createNote(ws, "Observability handbook", "")
	owner.createNote(ws, "Onboarding checklist", "")

	var resp skeinv1.SuggestTitlesResponse
	eventually(t, longWait, "a misspelt prefix suggests the title", func() (bool, string) {
		owner.call(http.MethodGet, "/v1/workspaces/"+ws+"/title-suggestions?prefix=obsevrab&limit=5", nil, &resp)
		return len(resp.GetSuggestions()) > 0 && resp.GetSuggestions()[0].GetNoteId() == want.GetId(), fmt.Sprintf("%v", resp.GetSuggestions())
	})
	if top := resp.GetSuggestions()[0]; top.GetEditDistance() == 0 || top.GetTitle() != "Observability handbook" {
		t.Errorf("top suggestion %v, want the handbook at a non-zero edit distance", top)
	}
	owner.call(http.MethodGet, "/v1/workspaces/"+ws+"/title-suggestions?prefix=Observ&limit=5", nil, &resp)
	if len(resp.GetSuggestions()) == 0 || resp.GetSuggestions()[0].GetEditDistance() != 0 {
		t.Errorf("an exact prefix: %v", resp.GetSuggestions())
	}
}
