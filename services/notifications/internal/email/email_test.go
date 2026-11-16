package email

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

var t0 = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

func notification(title, body, note string) domain.Notification {
	return domain.Notification{ID: "01936f60-5e6f-7a80-9b92-0d1e2f304152", Kind: domain.KindMention, Title: title, Body: body,
		Location: domain.Location{NoteID: note, BlockID: "9120384756102938475:3"}, CreatedAt: t0}
}

func renderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := NewRenderer("https://skein.example/app/")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSingleEmailHasTitleBodyAndNoteLinkInBothParts(t *testing.T) {
	msg, err := renderer(t).Render("send-1", Recipient{Name: "Ada", Address: "ada@example.com"},
		[]domain.Notification{notification("Grace mentioned you in Raft", "@ada check this", "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f")})
	if err != nil {
		t.Fatal(err)
	}
	link := "https://skein.example/app/notes/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f?block=9120384756102938475%3A3"
	for name, part := range map[string]string{"text": msg.Text, "html": msg.HTML} {
		for _, want := range []string{"Hi Ada", "Grace mentioned you in Raft", "@ada check this", "/settings/notifications"} {
			if !strings.Contains(part, want) {
				t.Errorf("%s part lacks %q:\n%s", name, want, part)
			}
		}
	}
	if !strings.Contains(msg.Text, link) || !strings.Contains(msg.HTML, strings.ReplaceAll(link, "&", "&amp;")) {
		t.Errorf("note link missing:\n%s\n%s", msg.Text, msg.HTML)
	}
	if msg.Subject != "Grace mentioned you in Raft" || msg.Count != 1 {
		t.Errorf("subject %q count %d", msg.Subject, msg.Count)
	}
}

func TestHTMLPartEscapesUserText(t *testing.T) {
	msg, err := renderer(t).Render("send-1", Recipient{Name: "<b>Ada</b>", Address: "ada@example.com"},
		[]domain.Notification{notification(`<script>alert(1)</script>`, `"><img src=x onerror=alert(1)>`, "")})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script>", "<img", "<b>Ada"} {
		if strings.Contains(msg.HTML, bad) {
			t.Errorf("HTML contains unescaped %q:\n%s", bad, msg.HTML)
		}
	}
	if !strings.Contains(msg.Text, "<script>") {
		t.Error("plain text part should carry the title verbatim")
	}
}

func TestDigestListsEveryNotificationAndLinksTheInbox(t *testing.T) {
	var ns []domain.Notification
	for _, title := range []string{"first", "second", "third"} {
		ns = append(ns, notification(title, strings.Repeat("long ", 200), ""))
	}
	msg, err := renderer(t).Render("send-2", Recipient{Address: "ada@example.com"}, ns)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Count != 3 || !strings.Contains(msg.Subject, "3 new notifications") {
		t.Errorf("subject %q count %d", msg.Subject, msg.Count)
	}
	for _, want := range []string{"first", "second", "third", "https://skein.example/app/inbox", "Hi there"} {
		if !strings.Contains(msg.Text, want) || !strings.Contains(msg.HTML, want) {
			t.Errorf("digest lacks %q", want)
		}
	}
	if strings.Count(msg.Text, "long") > 3*70 {
		t.Error("digest bodies are not shortened")
	}
}

func TestSubjectCannotInjectHeaders(t *testing.T) {
	msg, err := renderer(t).Render("send-3", Recipient{Name: "Ada\r\nBcc: x@evil.example", Address: "ada@example.com"},
		[]domain.Notification{notification("hello\r\nBcc: victim@example.com", "", "")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(msg, "Skein <notifications@skein.example>", t0)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Header.Get("Bcc"); got != "" {
		t.Fatalf("header injected: Bcc %q", got)
	}
}

func TestEncodedMessageParsesAsMultipartAlternativeWithStableMessageID(t *testing.T) {
	msg, err := renderer(t).Render("0193aa00-0000-7000-8000-000000000001", Recipient{Name: "Ada Lovelace", Address: "ada@example.com"},
		[]domain.Notification{notification("Grüße from Kyoto 京都", "line one\nline two", "")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(msg, "Skein <notifications@skein.example>", t0)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Encode(msg, "Skein <notifications@skein.example>", t0)
	if !bytes.Equal(raw, again) {
		t.Error("encoding is not deterministic")
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Header.Get("Message-ID"); got != "<0193aa00-0000-7000-8000-000000000001@notifications.skein>" {
		t.Errorf("Message-ID %q", got)
	}
	if got := parsed.Header.Get("Auto-Submitted"); got != "auto-generated" {
		t.Errorf("Auto-Submitted %q", got)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != "Grüße from Kyoto 京都" {
		t.Errorf("subject %q, %v", subject, err)
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("content type %q, %v", mediaType, err)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	var types []string
	for {
		p, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(p) // multipart decodes quoted-printable
		types = append(types, p.Header.Get("Content-Type"))
		if !strings.Contains(string(body), "Grüße from Kyoto 京都") {
			t.Errorf("part %s lost the title: %q", p.Header.Get("Content-Type"), body)
		}
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Errorf("parts %v", types)
	}
}

func TestEncodeRejectsBadAddresses(t *testing.T) {
	ok := Message{MessageID: "id", To: "ada@example.com", Subject: "s", Text: "t", HTML: "h"}
	if _, err := Encode(ok, "not an address", t0); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("bad sender: %v", err)
	}
	for _, to := range []string{"", "nobody", "Ada <ada@example.com>"} {
		m := ok
		m.To = to
		if _, err := Encode(m, "skein@example.com", t0); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("recipient %q: %v", to, err)
		}
	}
}

func TestRenderRefusesAnEmptyList(t *testing.T) {
	if _, err := renderer(t).Render("x", Recipient{Address: "a@example.com"}, nil); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("got %v", err)
	}
}

func TestNewRendererNeedsAnAbsoluteHTTPURL(t *testing.T) {
	for _, bad := range []string{"", "localhost:3000", "ftp://x", "/relative"} {
		if _, err := NewRenderer(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
