package app_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/email"
	"github.com/genuinebnt/skein-go/services/notifications/internal/fakes"
)

type mailHarness struct {
	*harness
	mailer *fakes.Mailer
	sender *app.EmailSender
}

func newMailHarness(t *testing.T, tweak func(*app.EmailSenderConfig)) *mailHarness {
	t.Helper()
	h := &mailHarness{harness: newHarness(t), mailer: &fakes.Mailer{}}
	renderer, err := email.NewRenderer("http://localhost:3000")
	if err != nil {
		t.Fatal(err)
	}
	cfg := app.EmailSenderConfig{Store: h.store, Directory: h.dir, Mailer: h.mailer, Renderer: renderer, Clock: h.clock.Now,
		NewID: fakes.IDs(2), Concurrency: 1, InitialBackoff: time.Minute, MaxBackoff: time.Hour, Lease: 2 * time.Minute,
		Jitter: func(d time.Duration) time.Duration { return d }}
	if tweak != nil {
		tweak(&cfg)
	}
	h.sender, err = app.NewEmailSender(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// request gives user a mention notification titled title, emailed by
// default.
func (h *mailHarness) request(user, title string) domain.Notification {
	h.t.Helper()
	h.mustApply(func(ev app.Event) (app.Outcome, error) {
		return h.producer.Request(context.Background(), ev, domain.Request{RecipientID: user, Kind: domain.KindMention, Title: title})
	}, grace, 0)
	for _, n := range h.store.Notifications() {
		if n.Title == title && n.RecipientID == user {
			return n
		}
	}
	h.t.Fatalf("no notification %q", title)
	return domain.Notification{}
}

func (h *mailHarness) pass() app.EmailPassResult {
	h.t.Helper()
	res, err := h.sender.RunOnce(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	h.check()
	return res
}

func TestNothingIsSentBeforeTheDigestDelay(t *testing.T) {
	h := newMailHarness(t, nil)
	h.request(ada, "one")
	h.clock.Advance(59 * time.Second)
	if res := h.pass(); res.Sent != 0 || len(h.mailer.Sent()) != 0 {
		t.Fatalf("sent early: %+v", res)
	}
	h.clock.Advance(time.Second)
	if res := h.pass(); res.Sent != 1 {
		t.Fatalf("not sent when due: %+v", res)
	}
	msg := h.mailer.Sent()[0]
	if msg.To != "ada@example.com" || msg.Subject != "one" || msg.Count != 1 {
		t.Errorf("message %+v", msg)
	}
	if res := h.pass(); res.Claimed != 0 || len(h.mailer.Sent()) != 1 {
		t.Fatalf("sent twice: %+v", res)
	}
}

func TestABurstBecomesOneDigestPerRecipient(t *testing.T) {
	h := newMailHarness(t, nil)
	for i := range 5 {
		h.request(ada, fmt.Sprintf("burst %d", i))
		h.clock.Advance(5 * time.Second)
	}
	h.request(grace, "for grace")
	h.clock.Advance(time.Minute)
	h.pass()
	sent := h.mailer.Sent()
	if len(sent) != 2 {
		t.Fatalf("%d emails, want a digest for ada and one for grace", len(sent))
	}
	var digest email.Message
	for _, m := range sent {
		if m.To == "ada@example.com" {
			digest = m
		}
	}
	if digest.Count != 5 || !strings.Contains(digest.Subject, "5 new notifications") {
		t.Fatalf("digest %+v", digest)
	}
	for i := range 5 {
		if !strings.Contains(digest.Text, fmt.Sprintf("burst %d", i)) {
			t.Errorf("digest lacks burst %d", i)
		}
	}
}

func TestDigestsAreSplitAtTheConfiguredSize(t *testing.T) {
	h := newMailHarness(t, func(c *app.EmailSenderConfig) { c.MaxPerEmail = 2; c.ClaimBatch = 10 })
	for i := range 5 {
		h.request(ada, fmt.Sprintf("n%d", i))
	}
	h.clock.Advance(time.Minute)
	h.pass()
	var counts []int
	for _, m := range h.mailer.Sent() {
		counts = append(counts, m.Count)
	}
	if fmt.Sprint(counts) != "[2 2 1]" {
		t.Fatalf("email sizes %v, want [2 2 1]", counts)
	}
}

func TestANotificationReadBeforeItsEmailIsNotEmailed(t *testing.T) {
	h := newMailHarness(t, nil)
	n := h.request(ada, "seen in the app")
	if err := h.inbox.MarkRead(context.Background(), ada, []string{n.ID}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	if res := h.pass(); res.Skipped != 1 || len(h.mailer.Sent()) != 0 {
		t.Fatalf("read notification emailed: %+v", res)
	}
}

func TestTransientFailuresRetryWithBackoffThenSucceed(t *testing.T) {
	failures := 2
	h := newMailHarness(t, nil)
	h.mailer.Fail = func(email.Message) error {
		if failures > 0 {
			failures--
			return errors.New("421 try again later")
		}
		return nil
	}
	h.request(ada, "flaky")
	h.clock.Advance(time.Minute)
	if res := h.pass(); res.Retried != 1 {
		t.Fatalf("first attempt: %+v", res)
	}
	h.clock.Advance(59 * time.Second)
	if res := h.pass(); res.Claimed != 0 {
		t.Fatalf("retried before the backoff: %+v", res)
	}
	h.clock.Advance(time.Second) // first retry after 1 minute
	if res := h.pass(); res.Retried != 1 {
		t.Fatalf("second attempt: %+v", res)
	}
	h.clock.Advance(2 * time.Minute) // then 2 minutes
	if res := h.pass(); res.Sent != 1 {
		t.Fatalf("third attempt: %+v", res)
	}
	sends := h.store.Sends()
	if len(sends) != 1 || sends[0].Attempts != 3 || sends[0].State != app.EmailSent {
		t.Fatalf("send %+v", sends)
	}
}

func TestPermanentFailuresAndExhaustedRetriesAreDeadLettered(t *testing.T) {
	cases := map[string]struct {
		fail   func(email.Message) error
		setup  func(h *mailHarness)
		passes int
	}{
		"smtp 5xx":          {fail: func(email.Message) error { return fmt.Errorf("%w: 550 no such mailbox", app.ErrPermanent) }, passes: 1},
		"retries exhausted": {fail: func(email.Message) error { return errors.New("timeout") }, passes: 3},
		"no address": {setup: func(h *mailHarness) {
			*h.dir = *fakes.NewDirectory(fakes.DirectoryUser{Profile: app.Profile{UserID: ada, DisplayName: "Ada"}})
		}, passes: 1},
		"unknown recipient": {setup: func(h *mailHarness) { *h.dir = *fakes.NewDirectory() }, passes: 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newMailHarness(t, func(cfg *app.EmailSenderConfig) { cfg.MaxAttempts = 3 })
			h.mailer.Fail = c.fail
			h.request(ada, "doomed")
			if c.setup != nil {
				c.setup(h)
			}
			for range c.passes {
				h.clock.Advance(time.Hour)
				h.pass()
			}
			sends := h.store.Sends()
			if len(sends) != 1 || sends[0].State != app.EmailDead || sends[0].LastError == "" {
				t.Fatalf("send %+v, want dead with its error", sends)
			}
			h.clock.Advance(24 * time.Hour)
			if res := h.pass(); res.Claimed != 0 {
				t.Fatal("a dead send was retried")
			}
		})
	}
}

func TestASendWhoseNotificationsWerePurgedIsDropped(t *testing.T) {
	h := newMailHarness(t, nil)
	h.mustApply(func(ev app.Event) (app.Outcome, error) {
		return h.producer.Request(context.Background(), ev, domain.Request{RecipientID: ada, Kind: domain.KindMention, Title: "in note",
			Location: domain.Location{NoteID: note}})
	}, grace, 0)
	h.clock.Advance(time.Minute)
	h.mailer.Fail = func(email.Message) error { return errors.New("down") }
	h.pass() // planned into a send, which then fails
	h.mustApply(func(ev app.Event) (app.Outcome, error) { return h.producer.NotePurged(context.Background(), ev, note) }, creator, 0)
	h.mailer.Fail = nil
	h.clock.Advance(time.Hour)
	if res := h.pass(); res.Dropped != 1 || len(h.mailer.Sent()) != 0 {
		t.Fatalf("purged notification emailed: %+v", res)
	}
}

func TestALateSuccessIsRecordedButALateFailureIsNot(t *testing.T) {
	h := newMailHarness(t, nil)
	h.request(ada, "contested")
	h.clock.Advance(time.Minute)
	renderer, err := email.NewRenderer("http://localhost:3000")
	if err != nil {
		t.Fatal(err)
	}
	takeoverMailer := &fakes.Mailer{Fail: func(email.Message) error { return errors.New("421 busy") }}
	takeover, err := app.NewEmailSender(app.EmailSenderConfig{Store: h.store, Directory: h.dir, Mailer: takeoverMailer,
		Renderer: renderer, Clock: h.clock.Now, NewID: fakes.IDs(3), Jitter: func(d time.Duration) time.Duration { return d }})
	if err != nil {
		t.Fatal(err)
	}
	var during app.EmailPassResult
	h.mailer.Accepted = func(email.Message) {
		// The first sender hangs after the SMTP server accepted: its lease
		// expires, another sender takes the send over and fails.
		h.mailer.Accepted = nil
		h.clock.Advance(3 * time.Minute)
		var err error
		if during, err = takeover.RunOnce(context.Background()); err != nil {
			t.Error(err)
		}
	}
	h.pass()
	if during.Retried != 1 {
		t.Fatalf("takeover pass: %+v", during)
	}
	sends := h.store.Sends()
	if len(sends) != 1 || sends[0].State != app.EmailSent || sends[0].Attempts != 2 {
		t.Fatalf("send %+v: the late success must win over the takeover's retry", sends)
	}
	h.clock.Advance(time.Hour)
	if res := h.pass(); res.Claimed != 0 || len(h.mailer.Sent()) != 1 {
		t.Fatalf("resent after a recorded success: %+v", res)
	}

	// And a late failure loses to the sender that took over and succeeded.
	h2 := newMailHarness(t, nil)
	h2.request(ada, "contested again")
	h2.clock.Advance(time.Minute)
	okMailer := &fakes.Mailer{}
	takeover2, _ := app.NewEmailSender(app.EmailSenderConfig{Store: h2.store, Directory: h2.dir, Mailer: okMailer,
		Renderer: renderer, Clock: h2.clock.Now, NewID: fakes.IDs(4)})
	h2.mailer.Fail = func(email.Message) error {
		h2.mailer.Fail = nil
		h2.clock.Advance(3 * time.Minute)
		if _, err := takeover2.RunOnce(context.Background()); err != nil {
			t.Error(err)
		}
		return errors.New("timeout")
	}
	h2.pass()
	if s := h2.store.Sends(); len(s) != 1 || s[0].State != app.EmailSent || len(okMailer.Sent()) != 1 {
		t.Fatalf("late failure overwrote the takeover's success: %+v", s)
	}
}

// TestEmailOutboxNeverLosesAndDuplicatesOnlyAfterACrashBetweenSendAndMark
// drives the sender through random transient and permanent SMTP failures
// and crashes (a commit that never happens) at every step, restarting the
// sender each pass, and checks the guarantee in the package documentation.
func TestEmailOutboxNeverLosesAndDuplicatesOnlyAfterACrashBetweenSendAndMark(t *testing.T) {
	seed := testSeed(t)
	r := rand.New(rand.NewPCG(seed, 13))
	h := newMailHarness(t, func(c *app.EmailSenderConfig) { c.MaxAttempts = 50; c.MaxPerEmail = 4 })
	crashedAfterAccept := map[string]int{}
	h.mailer.Fail = func(email.Message) error {
		switch x := r.IntN(100); {
		case x < 25:
			return errors.New("421 busy")
		case x < 27:
			return fmt.Errorf("%w: 550 rejected", app.ErrPermanent)
		}
		return nil
	}
	h.mailer.Accepted = func(msg email.Message) {
		if r.IntN(5) == 0 {
			h.store.FailNextCommits(1) // the "sent" mark never commits
			crashedAfterAccept[msg.MessageID]++
		}
	}
	users := []string{ada, grace, creator}
	for round := range 300 {
		if round < 200 && r.IntN(2) == 0 {
			h.request(users[r.IntN(len(users))], fmt.Sprintf("n%03d", round))
		}
		if r.IntN(10) == 0 {
			h.store.FailNextCommits(1) // crash while planning or claiming
		}
		h.clock.Advance(time.Duration(r.IntN(180)) * time.Second)
		_, _ = h.sender.RunOnce(context.Background()) // errors are the injected crashes
		h.store.FailNextCommits(0)
		h.check()
	}
	for range 200 { // drain without new faults
		h.mailer.Fail, h.mailer.Accepted = nil, nil
		h.clock.Advance(time.Hour)
		h.pass()
	}

	sentByID := map[string]int{}
	for _, m := range h.mailer.Sent() {
		sentByID[m.MessageID]++
	}
	sendState := map[string]app.EmailSendState{}
	for _, s := range h.store.Sends() {
		sendState[s.ID] = s.State
		if s.State != app.EmailSent && s.State != app.EmailDead {
			t.Fatalf("send %s left in %s", s.ID, s.State)
		}
	}
	notified := 0
	for _, j := range h.store.Jobs() {
		if j.State != fakes.JobAssigned {
			t.Fatalf("job %s left %s", j.ID, j.State)
		}
		notified++
		if sendState[j.SendID] == app.EmailSent && sentByID[j.SendID] == 0 {
			t.Fatalf("job %s: its send is marked sent but no email went out", j.ID)
		}
		if sendState[j.SendID] == app.EmailDead && sentByID[j.SendID] > 0 && crashedAfterAccept[j.SendID] == 0 {
			t.Fatalf("job %s: dead send %s had been accepted", j.ID, j.SendID)
		}
	}
	for id, n := range sentByID {
		if n > 1+crashedAfterAccept[id] {
			t.Fatalf("send %s went out %d times after %d crashes in the window", id, n, crashedAfterAccept[id])
		}
		if sendState[id] == "" {
			t.Fatalf("email %s does not belong to a send", id)
		}
	}
	if notified == 0 || len(sentByID) == 0 {
		t.Fatal("nothing was sent; the test proves nothing")
	}
	t.Logf("%d jobs, %d emails, %d accepted messages, %d crashes after accept", notified, len(sentByID), len(h.mailer.Sent()), len(crashedAfterAccept))
}
