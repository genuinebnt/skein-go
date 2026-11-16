// Package smtp implements app.Mailer over SMTP (Mailpit locally, on
// SMTP_ADDR; any submission server in production).
//
// # Design
//
// One connection per email: notification email is low volume, and a fresh
// connection never inherits a broken session. The whole exchange is bounded
// by the context's deadline (the sender's SendTimeout). STARTTLS is used
// whenever the server offers it, and required when SMTP_REQUIRE_TLS is set;
// PLAIN authentication is used when a username is configured (Go's
// net/smtp refuses to send it unencrypted except to localhost).
//
// The message counts as delivered when the server answers the end of DATA
// with 2xx: from then on it is the server's. QUIT's reply is ignored.
//
// Errors are classified for the sender's retry policy: a 5xx reply and a
// message that cannot be encoded wrap app.ErrPermanent; connection
// failures, timeouts and 4xx replies are transient.
//
// # Porting notes
//
// Rust: `lettre`'s SmtpTransport with the same STARTTLS policy;
// lettre's error type exposes the reply code for the same classification.
package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"time"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/email"
)

// Config configures the mailer.
type Config struct {
	// Addr is host:port of the SMTP server.
	Addr string
	// From is the sender, for example "Skein <notifications@skein.example>".
	From       string
	Username   string
	Password   config.Secret
	RequireTLS bool
	// HeloName is sent in EHLO; "localhost" when empty.
	HeloName string
	// DialTimeout bounds connecting when the context has no deadline; 10s.
	DialTimeout time.Duration
	// TLSConfig overrides the default (server name from Addr), for tests.
	TLSConfig *tls.Config
}

// LoadConfig reads SMTP_ADDR, SMTP_FROM, SMTP_USERNAME, SMTP_PASSWORD and
// SMTP_REQUIRE_TLS.
func LoadConfig(l *config.Loader) Config {
	cfg := Config{
		Addr:       l.String("SMTP_ADDR", "localhost:1025"),
		From:       l.String("SMTP_FROM", "Skein <notifications@skein.localhost>"),
		Username:   l.String("SMTP_USERNAME", ""),
		Password:   l.OptionalSecret("SMTP_PASSWORD"),
		RequireTLS: l.Bool("SMTP_REQUIRE_TLS", false),
		HeloName:   l.String("SMTP_HELO_NAME", ""),
	}
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		l.Fail("SMTP_ADDR", "must be host:port")
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		l.Fail("SMTP_FROM", "must be an email address")
	}
	return cfg
}

// Mailer sends through one SMTP server.
type Mailer struct {
	cfg  Config
	host string
	now  func() time.Time
}

// New validates cfg and returns a mailer.
func New(cfg Config) (*Mailer, error) {
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("smtp: address %q: %w", cfg.Addr, err)
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		return nil, fmt.Errorf("smtp: sender %q: %w", cfg.From, err)
	}
	if cfg.HeloName == "" {
		cfg.HeloName = "localhost"
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	return &Mailer{cfg: cfg, host: host, now: time.Now}, nil
}

// Send implements app.Mailer.
func (m *Mailer) Send(ctx context.Context, msg email.Message) error {
	raw, err := email.Encode(msg, m.cfg.From, m.now())
	if err != nil {
		return fmt.Errorf("%w: %w", app.ErrPermanent, err)
	}
	from, _ := mail.ParseAddress(m.cfg.From)

	dialer := net.Dialer{Timeout: m.cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", m.cfg.Addr)
	if err != nil {
		return fmt.Errorf("smtp: connect: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Closing the connection when ctx ends unblocks any read in progress.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return classify("greeting", err)
	}
	if err := c.Hello(m.cfg.HeloName); err != nil {
		return classify("EHLO", err)
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		tlsCfg := m.cfg.TLSConfig
		if tlsCfg == nil {
			tlsCfg = &tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return classify("STARTTLS", err)
		}
	} else if m.cfg.RequireTLS {
		return fmt.Errorf("%w: smtp: server does not offer STARTTLS and SMTP_REQUIRE_TLS is set", app.ErrPermanent)
	}
	if m.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password.Reveal(), m.host)); err != nil {
			return classify("AUTH", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return classify("MAIL FROM", err)
	}
	if err := c.Rcpt(msg.To); err != nil {
		return classify("RCPT TO", err)
	}
	w, err := c.Data()
	if err != nil {
		return classify("DATA", err)
	}
	if _, err := w.Write(raw); err != nil {
		return classify("DATA", err)
	}
	if err := w.Close(); err != nil {
		return classify("end of DATA", err)
	}
	// Accepted. A failing QUIT changes nothing.
	_ = c.Quit()
	return nil
}

// classify wraps 5xx replies as permanent; everything else is transient.
func classify(step string, err error) error {
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 500 {
		return fmt.Errorf("%w: smtp %s: %d %s", app.ErrPermanent, step, reply.Code, reply.Msg)
	}
	return fmt.Errorf("smtp %s: %w", step, err)
}
