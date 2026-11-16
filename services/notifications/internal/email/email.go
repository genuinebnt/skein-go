// Package email renders notification emails, plain text and HTML, for one
// notification or a digest of several, and encodes them as RFC 5322
// messages for SMTP.
//
// # Design
//
// Templates are embedded: text/template for the plain part, html/template
// for the HTML part, so every value placed in HTML is escaped by context
// (titles and bodies come from users and must never become markup). Links
// point at the web app under a configured base URL: a note's page, with
// the block as a query parameter, or the inbox when the notification has
// no location.
//
// The encoded message is multipart/alternative with both parts in
// quoted-printable UTF-8, a Message-ID derived from the email send's ID
// (so a re-send after a crash is the same message and a client can
// deduplicate it) and Auto-Submitted: auto-generated (RFC 3834), which
// keeps vacation responders from answering. Header values are stripped of
// control characters, so no title can inject a header.
//
// # Porting notes
//
// Rust: `askama` or `minijinja` (with auto-escaping for the HTML) for the
// templates and `lettre`'s message builder for the MIME structure. The
// boundary is derived from the Message-ID so encoded output is
// deterministic and testable byte for byte.
package email

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/url"
	"strings"
	texttemplate "text/template"
	"time"
	"unicode"

	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// ErrInvalidMessage means a message cannot be encoded, for example because
// its recipient address is malformed. Retrying will not help.
var ErrInvalidMessage = errors.New("email: invalid message")

// Message is a rendered email.
type Message struct {
	// MessageID is the bare ID; Encode wraps it as <id@domain>.
	MessageID string
	To        string
	ToName    string
	Subject   string
	Text      string
	HTML      string
	// Count is how many notifications the email carries.
	Count int
}

// Recipient is who an email goes to.
type Recipient struct {
	Name    string
	Address string
}

// Limits on what one email shows, in code points.
const (
	maxSubjectLength    = 150
	maxDigestBodyLength = 300
)

//go:embed templates/*.tmpl
var templateFiles embed.FS

// Renderer turns notifications into messages.
type Renderer struct {
	base       *url.URL
	singleText *texttemplate.Template
	singleHTML *htmltemplate.Template
	digestText *texttemplate.Template
	digestHTML *htmltemplate.Template
}

// NewRenderer parses the templates. baseURL is the web app's origin, for
// example http://localhost:3000.
func NewRenderer(baseURL string) (*Renderer, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("email: base URL %q must be an absolute http(s) URL", baseURL)
	}
	r := &Renderer{base: base}
	parseText := func(name string) (*texttemplate.Template, error) {
		return texttemplate.New(name).ParseFS(templateFiles, "templates/"+name)
	}
	parseHTML := func(name string) (*htmltemplate.Template, error) {
		return htmltemplate.New(name).ParseFS(templateFiles, "templates/"+name)
	}
	var errs [4]error
	r.singleText, errs[0] = parseText("single.txt.tmpl")
	r.singleHTML, errs[1] = parseHTML("single.html.tmpl")
	r.digestText, errs[2] = parseText("digest.txt.tmpl")
	r.digestHTML, errs[3] = parseHTML("digest.html.tmpl")
	if err := errors.Join(errs[:]...); err != nil {
		return nil, fmt.Errorf("email: templates: %w", err)
	}
	return r, nil
}

type item struct {
	Title, Body, Link string
}

type view struct {
	Subject       string
	RecipientName string
	Item          item
	Items         []item
	InboxLink     string
	SettingsLink  string
}

// Render builds the email for notifications (oldest first): a plain email
// for one, a digest for more. It returns an error for an empty list.
func (r *Renderer) Render(messageID string, to Recipient, notifications []domain.Notification) (Message, error) {
	if len(notifications) == 0 {
		return Message{}, fmt.Errorf("%w: no notifications", ErrInvalidMessage)
	}
	v := view{RecipientName: to.Name, InboxLink: r.link("/inbox", nil), SettingsLink: r.link("/settings/notifications", nil)}
	if v.RecipientName == "" {
		v.RecipientName = "there"
	}
	for _, n := range notifications {
		body := n.Body
		if len(notifications) > 1 {
			body = domain.Truncate(body, maxDigestBodyLength)
		}
		v.Items = append(v.Items, item{Title: n.Title, Body: body, Link: r.notificationLink(n)})
	}
	textTmpl, htmlTmpl := r.digestText, r.digestHTML
	if len(notifications) == 1 {
		v.Subject = headerSafe(notifications[0].Title)
		v.Item = v.Items[0]
		textTmpl, htmlTmpl = r.singleText, r.singleHTML
	} else {
		v.Subject = fmt.Sprintf("You have %d new notifications in Skein", len(notifications))
	}
	var text, html bytes.Buffer
	if err := textTmpl.Execute(&text, v); err != nil {
		return Message{}, fmt.Errorf("email: render text: %w", err)
	}
	if err := htmlTmpl.Execute(&html, v); err != nil {
		return Message{}, fmt.Errorf("email: render html: %w", err)
	}
	return Message{MessageID: messageID, To: to.Address, ToName: headerSafe(to.Name), Subject: v.Subject,
		Text: text.String(), HTML: html.String(), Count: len(notifications)}, nil
}

func (r *Renderer) notificationLink(n domain.Notification) string {
	if n.Location.NoteID == "" {
		return r.link("/inbox", nil)
	}
	q := url.Values{}
	if n.Location.BlockID != "" {
		q.Set("block", n.Location.BlockID)
	}
	return r.link("/notes/"+url.PathEscape(n.Location.NoteID), q)
}

func (r *Renderer) link(path string, q url.Values) string {
	u := *r.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	return u.String()
}

// headerSafe replaces control characters (CR and LF above all) with spaces
// and bounds the length, so a value cannot add or break headers.
func headerSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return domain.Truncate(strings.TrimSpace(s), maxSubjectLength)
}

// MessageIDDomain is the right-hand side of every Message-ID.
const MessageIDDomain = "notifications.skein"

// Encode returns msg as an RFC 5322 message from the given sender.
func Encode(msg Message, from string, date time.Time) ([]byte, error) {
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("%w: sender address: %v", ErrInvalidMessage, err)
	}
	if _, err := mail.ParseAddress(msg.To); err != nil || strings.ContainsAny(msg.To, "<>") {
		return nil, fmt.Errorf("%w: recipient address", ErrInvalidMessage)
	}
	if msg.MessageID == "" || strings.ContainsAny(msg.MessageID, "<>@ \r\n") {
		return nil, fmt.Errorf("%w: message id", ErrInvalidMessage)
	}
	text, err := qpEncode(msg.Text)
	if err != nil {
		return nil, err
	}
	html, err := qpEncode(msg.HTML)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(msg.MessageID))
	boundary := "skein-" + hex.EncodeToString(sum[:12])
	for strings.Contains(text, boundary) || strings.Contains(html, boundary) {
		boundary += "x"
	}
	to := mail.Address{Name: headerSafe(msg.ToName), Address: msg.To}
	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", sender.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", headerSafe(msg.Subject)))
	header("Date", date.UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+msg.MessageID+"@"+MessageIDDomain+">")
	header("Auto-Submitted", "auto-generated")
	header("MIME-Version", "1.0")
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	part := func(contentType, body string) {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n", boundary, contentType, body)
	}
	part("text/plain", text)
	part("text/html", html)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}

func qpEncode(s string) (string, error) {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	normalised := strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	if _, err := w.Write([]byte(normalised)); err != nil {
		return "", fmt.Errorf("email: encode body: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("email: encode body: %w", err)
	}
	return b.String(), nil
}
