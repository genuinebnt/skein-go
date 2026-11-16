package httpapi

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/edge"
)

//go:embed openapi/skein.swagger.json
var openAPIDocument []byte

func serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	edge.LabelRoute(r.Context(), r.Method, "/openapi.json")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(openAPIDocument)
}

type docsOperation struct {
	Method, Path, Summary, Tag string
}

type docsPage struct {
	Title, Description string
	Operations         []docsOperation
}

var (
	docsOnce     sync.Once
	docsRendered []byte
)

var docsTemplate = template.Must(template.New("docs").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font:15px/1.5 system-ui,sans-serif;max-width:60rem;margin:2rem auto;padding:0 1rem}
code{font-family:ui-monospace,monospace}td{padding:.2rem .8rem .2rem 0;vertical-align:top}
.m{font-weight:600}</style></head><body>
<h1>{{.Title}}</h1>
<p>The machine-readable description is <a href="/openapi.json">/openapi.json</a> (OpenAPI 2.0).
WebSocket endpoints (<code>/v1/notes/{noteId}/sync</code>, <code>/v1/chats/{chatId}/messages</code>,
<code>/v1/notifications/live</code>) are described in docs/api/websocket.md.</p>
<table>{{range .Operations}}<tr><td class="m">{{.Method}}</td><td><code>{{.Path}}</code></td><td>{{.Summary}}</td><td>{{.Tag}}</td></tr>{{end}}</table>
</body></html>`))

// renderDocs builds the page once from the embedded document. A document
// that does not parse is a build defect caught by the tests; the page then
// shows only the header.
func renderDocs() []byte {
	docsOnce.Do(func() {
		var doc struct {
			Info struct {
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"info"`
			Paths map[string]map[string]struct {
				Summary string   `json:"summary"`
				Tags    []string `json:"tags"`
			} `json:"paths"`
		}
		_ = json.Unmarshal(openAPIDocument, &doc)
		page := docsPage{Title: doc.Info.Title, Description: doc.Info.Description}
		for path, methods := range doc.Paths {
			for method, op := range methods {
				tag := ""
				if len(op.Tags) > 0 {
					tag = op.Tags[0]
				}
				page.Operations = append(page.Operations, docsOperation{strings.ToUpper(method), path, op.Summary, tag})
			}
		}
		sort.Slice(page.Operations, func(i, j int) bool {
			a, b := page.Operations[i], page.Operations[j]
			if a.Tag != b.Tag {
				return a.Tag < b.Tag
			}
			if a.Path != b.Path {
				return a.Path < b.Path
			}
			return a.Method < b.Method
		})
		var sb strings.Builder
		_ = docsTemplate.Execute(&sb, page)
		docsRendered = []byte(sb.String())
	})
	return docsRendered
}

func serveDocs(w http.ResponseWriter, r *http.Request) {
	edge.LabelRoute(r.Context(), r.Method, "/docs")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page has one inline style block and nothing else.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	_, _ = w.Write(renderDocs())
}
