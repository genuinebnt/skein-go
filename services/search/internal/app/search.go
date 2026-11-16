package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/genuinebnt/skein-go/libs/trigram"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// Mode is the search mode.
type Mode uint8

const (
	ModeText  Mode = 1
	ModeRegex Mode = 2
)

// Page sizes, as in docs/api/README.md: 0 means the default; larger than
// the maximum is clamped.
const (
	DefaultPageSize   = 50
	MaxPageSize       = 500
	DefaultTitleLimit = 10
	MaxTitleLimit     = 50
	// ResultTTL is short: a cached page is keyed by the workspace version,
	// so it is never served after the workspace changes on this replica,
	// and tag invalidation removes pages that show a changed or purged note
	// on every replica. The TTL only bounds what a lost invalidation leaves.
	ResultTTL = 20 * time.Second
	// MaxQueryLength guards the regex compiler and the term parser.
	MaxQueryLength = 1000
)

// SearchRequest is a Search call.
type SearchRequest struct {
	Subject       auth.Subject
	WorkspaceID   string
	Query         string
	Mode          Mode
	CaseSensitive bool
	WholeWord     bool
	CodeOnly      bool
	Languages     []string
	PageSize      int
	PageToken     string
}

// SearchPage is a page of results.
type SearchPage struct {
	Hits          []domain.Hit
	NextPageToken string
	Candidates    int
	Matched       int
}

// Searcher implements SearchService.
type Searcher struct {
	index   *Index
	access  *Access
	results *redisx.Cache[SearchPage]
}

// NewSearcher returns a Searcher caching pages on store.
func NewSearcher(index *Index, access *Access, store redisx.Store) (*Searcher, error) {
	results, err := redisx.New[SearchPage](store, redisx.Options[SearchPage]{Name: "search-results", TTL: ResultTTL})
	if err != nil {
		return nil, err
	}
	return &Searcher{index: index, access: access, results: results}, nil
}

// cursor is the decoded page token: the sort key of the last hit served
// and a fingerprint of the request it belongs to.
type cursor struct {
	Fingerprint string  `json:"f"`
	Score       float64 `json:"s"`
	Key         string  `json:"k"`
}

func encodeCursor(c cursor) string {
	data, _ := json.Marshal(c) // a struct of plain fields always marshals
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(token, fingerprint string) (cursor, error) {
	var c cursor
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	if err != nil {
		return cursor{}, invalid("page.page_token", "malformed page token")
	}
	if c.Fingerprint != fingerprint {
		return cursor{}, invalid("page.page_token", "page token belongs to a different request")
	}
	return c, nil
}

// fingerprint identifies the request's parameters other than paging, so a
// token cannot be replayed against another query.
func (r SearchRequest) fingerprint() string {
	langs := slices.Clone(r.Languages)
	for i, l := range langs {
		langs[i] = trigram.NormalizeLanguage(l)
	}
	slices.Sort(langs)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%s\x00%t%t%t\x00%s", r.WorkspaceID, r.Mode, r.Query, r.CaseSensitive, r.WholeWord, r.CodeOnly, strings.Join(langs, "\x00"))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func clampPageSize(n int) int {
	switch {
	case n <= 0:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	}
	return n
}

func (r *SearchRequest) validate() error {
	switch {
	case r.WorkspaceID == "":
		return invalid("workspace_id", "is required")
	case strings.TrimSpace(r.Query) == "":
		return invalid("query", "is required")
	case len(r.Query) > MaxQueryLength:
		return invalid("query", "is longer than %d bytes", MaxQueryLength)
	}
	if r.Mode == 0 {
		r.Mode = ModeText
	}
	if r.Mode != ModeText && r.Mode != ModeRegex {
		return invalid("mode", "unknown mode")
	}
	r.PageSize = clampPageSize(r.PageSize)
	return nil
}

// Search runs a text or regex search over the notes the subject can read.
//
// Order: the readable set comes first and every candidate is intersected
// with it, so unreadable notes are never read, ranked or counted. Text
// hits are notes ranked by score; regex hits are blocks in note and block
// order with score 1. Pages are cursor-based: the token holds the last
// hit's score and key, so hits added while paging never shift later
// pages.
//
// Pages are cached per user, workspace version and request (ResultTTL),
// tagged with the user, the workspace and every note on the page.
func (s *Searcher) Search(ctx context.Context, req SearchRequest) (SearchPage, error) {
	if err := req.validate(); err != nil {
		return SearchPage{}, err
	}
	fp := req.fingerprint()
	var after *cursor
	if req.PageToken != "" {
		c, err := decodeCursor(req.PageToken, fp)
		if err != nil {
			return SearchPage{}, err
		}
		after = &c
	}
	var re *trigram.Regexp
	if req.Mode == ModeRegex {
		var err error
		if re, err = trigram.CompileRegexp(req.Query, req.CaseSensitive); err != nil {
			return SearchPage{}, invalid("query", "%v", err)
		}
	}
	readable, err := s.access.Readable(ctx, req.Subject, req.WorkspaceID)
	if err != nil {
		return SearchPage{}, err
	}
	var version int64
	_ = s.index.Read(func(p *domain.Projection) error {
		if w, ok := p.Workspace(req.WorkspaceID); ok {
			version = w.Version
		}
		return nil
	})
	key := fmt.Sprintf("%s|%s|%d|%s|%d|%s", req.Subject.UserID, req.WorkspaceID, version, fp, req.PageSize, req.PageToken)
	return s.results.GetOrLoad(ctx, key, func(ctx context.Context) (SearchPage, []string, error) {
		page, err := s.search(req, re, readable, after, fp)
		if err != nil {
			return SearchPage{}, nil, err
		}
		tags := []string{UserTag(req.Subject.UserID), WorkspaceTag(req.WorkspaceID)}
		for _, h := range page.Hits {
			tags = append(tags, noteTag(h.Note))
		}
		return page, tags, nil
	})
}

// rankedItem is one hit before rendering, with its sort key.
type rankedItem struct {
	score  float64
	key    string
	render func() domain.Hit
}

// afterCursor reports whether item sorts after c (score descending, then
// key ascending).
func (it rankedItem) afterCursor(c *cursor) bool {
	return c == nil || it.score < c.Score || it.score == c.Score && it.key > c.Key
}

func (s *Searcher) search(req SearchRequest, re *trigram.Regexp, readable domain.Readable, after *cursor, fp string) (SearchPage, error) {
	var page SearchPage
	err := s.index.Read(func(p *domain.Projection) error {
		w, ok := p.Workspace(req.WorkspaceID)
		if !ok {
			return nil
		}
		f := domain.Filters{CaseSensitive: req.CaseSensitive, WholeWord: req.WholeWord, CodeOnly: req.CodeOnly, Languages: req.Languages}
		var items []rankedItem
		if req.Mode == ModeRegex {
			res := w.SearchRegex(re, readable, f)
			page.Candidates, page.Matched = res.Candidates, len(res.Matches)
			for _, m := range res.Matches {
				items = append(items, rankedItem{score: 1, key: fmt.Sprintf("%s\x00%010d", m.Ref.Note, m.Ordinal), render: func() domain.Hit { return w.RenderRegex(m) }})
			}
		} else {
			res, err := w.SearchText(req.Query, readable, f)
			if err != nil {
				return invalid("query", "%v", err)
			}
			page.Candidates, page.Matched = res.Candidates, res.Matched
			for _, m := range res.Matches {
				items = append(items, rankedItem{score: m.Score, key: m.Note, render: func() domain.Hit { return w.RenderText(res, m) }})
			}
		}
		start := 0
		for start < len(items) && !items[start].afterCursor(after) {
			start++
		}
		end := min(len(items), start+req.PageSize)
		for _, it := range items[start:end] {
			page.Hits = append(page.Hits, it.render())
		}
		if end < len(items) {
			last := items[end-1]
			page.NextPageToken = encodeCursor(cursor{Fingerprint: fp, Score: last.score, Key: last.key})
		}
		return nil
	})
	return page, err
}

// TitlesRequest is a SuggestTitles call.
type TitlesRequest struct {
	Subject     auth.Subject
	WorkspaceID string
	Prefix      string
	Limit       int
}

// SuggestTitles completes a typed prefix to readable note titles.
func (s *Searcher) SuggestTitles(ctx context.Context, req TitlesRequest) ([]domain.TitleSuggestion, error) {
	switch {
	case req.WorkspaceID == "":
		return nil, invalid("workspace_id", "is required")
	case strings.TrimSpace(req.Prefix) == "":
		return nil, invalid("prefix", "is required")
	case len(req.Prefix) > MaxQueryLength:
		return nil, invalid("prefix", "is longer than %d bytes", MaxQueryLength)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultTitleLimit
	}
	limit = min(limit, MaxTitleLimit)
	readable, err := s.access.Readable(ctx, req.Subject, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var out []domain.TitleSuggestion
	err = s.index.Read(func(p *domain.Projection) error {
		if w, ok := p.Workspace(req.WorkspaceID); ok {
			out = w.SuggestTitles(req.Prefix, readable, limit)
		}
		return nil
	})
	return out, err
}
