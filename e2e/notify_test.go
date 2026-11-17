//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

// liveNotifications opens u's notification socket and relays what arrives.
func liveNotifications(t *testing.T, u *user) (<-chan *skeinv1.Notification, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	defer dialCancel()
	conn, err := u.dial(dialCtx, "/v1/notifications/live")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out := make(chan *skeinv1.Notification, 16)
	go func() {
		defer close(out)
		for {
			var resp skeinv1.SubscribeResponse
			if err := readProto(ctx, conn, &resp); err != nil {
				return
			}
			out <- resp.GetNotification()
		}
	}()
	return out, func() { cancel(); _ = conn.Close(websocket.StatusNormalClosure, "") }
}

// mailpitMessages lists the messages Mailpit holds for an address.
func mailpitMessages(t *testing.T, to string) []struct {
	Subject string `json:"Subject"`
	Snippet string `json:"Snippet"`
} {
	t.Helper()
	resp, err := httpClient.Get(mailpitURL + "/api/v1/search?query=" + url.QueryEscape("to:"+to))
	if err != nil {
		t.Fatalf("mailpit: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Messages []struct {
			Subject string `json:"Subject"`
			Snippet string `json:"Snippet"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("mailpit: %v", err)
	}
	return body.Messages
}

func TestAMentionNotifiesLiveInTheInboxAndByEmail(t *testing.T) {
	t.Parallel()
	author, ada := signUp(t, "author"), signUp(t, "ada")
	_, note := sharedNote(t, author, "Review notes", ada)
	live, stop := liveNotifications(t, ada)
	defer stop()

	r := newReplica(t, author, note.GetId()).mustConnect()
	defer r.disconnect()
	eventually(t, longWait, "the author's replica has the note", func() (bool, string) { return r.length() > 0, r.text() })
	r.appendText(fmt.Sprintf(" @%s can you check the snapshot threshold?", ada.handle()))

	var got *skeinv1.Notification
	select {
	case got = <-live:
	case <-time.After(longWait):
		t.Fatalf("no live notification; inbox: %s", ada.do(http.MethodGet, "/v1/notifications", nil).body)
	}
	if got.GetKind() != skeinv1.NotificationKind_NOTIFICATION_KIND_MENTION || got.GetLocation().GetNoteId() != note.GetId() || got.GetActorId() != author.id {
		t.Errorf("live notification %v, want a mention in the note by the author", got)
	}

	var inbox skeinv1.ListInboxResponse
	ada.call(http.MethodGet, "/v1/notifications?unreadOnly=true", nil, &inbox)
	if inbox.GetUnread() < 1 || len(inbox.GetNotifications()) == 0 || inbox.GetNotifications()[0].GetId() != got.GetId() {
		t.Errorf("inbox %v, want the mention unread", &inbox)
	}
	ada.call(http.MethodPost, "/v1/notifications:markRead", &skeinv1.MarkReadRequest{Ids: []string{got.GetId()}}, nil)
	ada.call(http.MethodGet, "/v1/notifications?unreadOnly=true", nil, &inbox)
	if inbox.GetUnread() != 0 {
		t.Errorf("after markRead the inbox has %d unread", inbox.GetUnread())
	}

	// Mentions email by default, after the digest delay (1 minute). Marking
	// it read in the app skips the email, so a second mention, left unread,
	// is the one that must arrive. Mentions are diffed per block, so it goes
	// in a new block (U+001E starts one).
	r.appendText(fmt.Sprintf("\u001eAlso @%s, the compaction budget.", ada.handle()))
	eventually(t, 3*time.Minute, "the mention email reaches the SMTP sink", func() (bool, string) {
		msgs := mailpitMessages(t, ada.email)
		return len(msgs) > 0, fmt.Sprintf("%d messages", len(msgs))
	})
	msg := mailpitMessages(t, ada.email)[0]
	if !strings.Contains(strings.ToLower(msg.Subject+msg.Snippet), "mention") {
		t.Errorf("email %q / %q does not read as a mention", msg.Subject, msg.Snippet)
	}
}
