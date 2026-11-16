package config

import (
	"fmt"
	"log/slog"
)

const redacted = "[REDACTED]"

// Secret is a credential read from configuration. Every way of printing,
// logging or encoding it yields "[REDACTED]"; call Reveal at the one place
// the plaintext is needed (opening a connection, signing a token).
//
// The zero value is the empty secret.
type Secret struct {
	value string
}

// NewSecret wraps a value that did not come from a Loader, for example one
// fetched from Secret Manager at startup.
func NewSecret(value string) Secret { return Secret{value: value} }

// Reveal returns the plaintext.
func (s Secret) Reveal() string { return s.value }

// IsSet reports whether the secret is non-empty.
func (s Secret) IsSet() bool { return s.value != "" }

// String implements fmt.Stringer.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer so %#v does not print the field.
func (Secret) GoString() string { return redacted }

// Format implements fmt.Formatter so every verb, including %x and %q, is
// redacted.
func (Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON implements json.Marshaler.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText implements encoding.TextMarshaler, which covers encoders that
// prefer text (YAML, TOML, map keys).
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
