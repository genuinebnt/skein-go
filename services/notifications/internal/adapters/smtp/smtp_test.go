package smtp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/email"
)

// server is a minimal in-process SMTP server: enough of RFC 5321 for
// net/smtp, with scripted replies per command.
type server struct {
	t        *testing.T
	ln       net.Listener
	replies  map[string]string // command -> reply overriding the default
	stall    bool              // never answer the greeting
	mu       sync.Mutex
	messages []string
	rcpts    []string
}

func newServer(t *testing.T, replies map[string]string) *server {
	t.Helper()
	return startServer(t, replies, false)
}

func startServer(t *testing.T, replies map[string]string, stall bool) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{t: t, ln: ln, replies: replies, stall: stall}
	t.Cleanup(func() { _ = ln.Close() })
	go s.serve()
	return s
}

func (s *server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *server) handle(conn net.Conn) {
	defer conn.Close()
	if s.stall {
		time.Sleep(time.Second)
		return
	}
	r := bufio.NewReader(conn)
	reply := func(cmd, def string) { fmt.Fprintf(conn, "%s\r\n", s.replyFor(cmd, def)) }
	reply("greeting", "220 test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.Fields(line + " x")[0])
		switch cmd {
		case "EHLO":
			fmt.Fprintf(conn, "250-test\r\n250 8BITMIME\r\n")
		case "MAIL":
			reply("MAIL", "250 ok")
		case "RCPT":
			s.mu.Lock()
			s.rcpts = append(s.rcpts, strings.TrimSpace(line))
			s.mu.Unlock()
			reply("RCPT", "250 ok")
		case "DATA":
			reply("DATA", "354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			final := s.replyFor("END", "250 queued")
			if strings.HasPrefix(final, "250") {
				s.mu.Lock()
				s.messages = append(s.messages, b.String())
				s.mu.Unlock()
			}
			fmt.Fprintf(conn, "%s\r\n", final)
		case "QUIT":
			reply("QUIT", "221 bye")
			return
		default:
			fmt.Fprintf(conn, "502 not implemented\r\n")
		}
	}
}

func (s *server) replyFor(cmd, def string) string {
	if r, ok := s.replies[cmd]; ok {
		return r
	}
	return def
}

func (s *server) accepted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.messages...)
}

func message() email.Message {
	return email.Message{MessageID: "0193aa00-0000-7000-8000-000000000001", To: "ada@example.com", ToName: "Ada",
		Subject: "Grace mentioned you", Text: "hello", HTML: "<p>hello</p>", Count: 1}
}

func mailer(t *testing.T, addr string, tweak func(*Config)) *Mailer {
	t.Helper()
	cfg := Config{Addr: addr, From: "Skein <notifications@skein.example>"}
	if tweak != nil {
		tweak(&cfg)
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSendDeliversAParsableMessageToTheRecipient(t *testing.T) {
	s := newServer(t, nil)
	if err := mailer(t, s.ln.Addr().String(), nil).Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	got := s.accepted()
	if len(got) != 1 {
		t.Fatalf("%d messages accepted", len(got))
	}
	parsed, err := mail.ReadMessage(strings.NewReader(got[0]))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("Message-ID") != "<0193aa00-0000-7000-8000-000000000001@notifications.skein>" {
		t.Errorf("headers %v", parsed.Header)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rcpts) != 1 || !strings.Contains(s.rcpts[0], "<ada@example.com>") {
		t.Errorf("RCPT %v", s.rcpts)
	}
}

func TestRepliesAreClassifiedForRetry(t *testing.T) {
	cases := map[string]struct {
		replies   map[string]string
		permanent bool
	}{
		"mailbox unknown":   {map[string]string{"RCPT": "550 no such user"}, true},
		"greylisted":        {map[string]string{"RCPT": "451 try later"}, false},
		"rejected content":  {map[string]string{"END": "554 rejected"}, true},
		"queue full":        {map[string]string{"END": "452 insufficient storage"}, false},
		"sender refused":    {map[string]string{"MAIL": "553 not allowed"}, true},
		"service shut down": {map[string]string{"greeting": "421 shutting down"}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newServer(t, c.replies)
			err := mailer(t, s.ln.Addr().String(), nil).Send(context.Background(), message())
			if err == nil {
				t.Fatal("send succeeded")
			}
			if errors.Is(err, app.ErrPermanent) != c.permanent {
				t.Fatalf("permanent=%t for %v", !c.permanent, err)
			}
			if len(s.accepted()) != 0 {
				t.Fatal("a refused message was accepted")
			}
		})
	}
}

func TestUnreachableServerIsTransient(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	err := mailer(t, addr, nil).Send(context.Background(), message())
	if err == nil || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("got %v, want a transient error", err)
	}
}

func TestAStalledServerIsCutOffAtTheDeadline(t *testing.T) {
	s := startServer(t, nil, true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := mailer(t, s.ln.Addr().String(), nil).Send(ctx, message())
	if err == nil || errors.Is(err, app.ErrPermanent) || time.Since(start) > 800*time.Millisecond {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
}

func TestBadRecipientAddressIsPermanentWithoutConnecting(t *testing.T) {
	msg := message()
	msg.To = "not an address"
	err := mailer(t, "127.0.0.1:1", nil).Send(context.Background(), msg)
	if !errors.Is(err, app.ErrPermanent) {
		t.Fatalf("got %v", err)
	}
}

func TestRequireTLSRefusesAPlaintextServer(t *testing.T) {
	s := newServer(t, nil)
	err := mailer(t, s.ln.Addr().String(), func(c *Config) { c.RequireTLS = true }).Send(context.Background(), message())
	if !errors.Is(err, app.ErrPermanent) || len(s.accepted()) != 0 {
		t.Fatalf("got %v with %d accepted", err, len(s.accepted()))
	}
}
