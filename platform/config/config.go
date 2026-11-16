package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Errors recorded by a Loader. Test with errors.Is; each is wrapped with the
// variable name.
var (
	// ErrMissing means a required variable is unset or empty.
	ErrMissing = errors.New("config: required variable is not set")
	// ErrInvalid means a variable is set but cannot be parsed or fails
	// validation.
	ErrInvalid = errors.New("config: invalid value")
)

// Lookup returns the value of an environment variable and whether it is set.
// os.LookupEnv has this signature.
type Lookup func(key string) (string, bool)

// Loader reads typed values and accumulates errors. It is not safe for
// concurrent use; configuration is read once at startup.
type Loader struct {
	lookup Lookup
	errs   []error
}

// New returns a Loader reading from lookup.
func New(lookup Lookup) *Loader { return &Loader{lookup: lookup} }

// FromEnv returns a Loader reading the process environment.
func FromEnv() *Loader { return New(os.LookupEnv) }

// FromMap returns a Loader reading from m, for tests and examples.
func FromMap(m map[string]string) *Loader {
	return New(func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	})
}

// Err returns every error recorded so far, joined, or nil.
func (l *Loader) Err() error { return errors.Join(l.errs...) }

// Fail records a validation error for key. Use it for checks that span
// several variables, for example "MIN must not exceed MAX".
func (l *Loader) Fail(key, reason string) {
	l.errs = append(l.errs, fmt.Errorf("%w: %s: %s", ErrInvalid, key, reason))
}

func (l *Loader) raw(key string) (string, bool) {
	v, ok := l.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

func (l *Loader) missing(key string) {
	l.errs = append(l.errs, fmt.Errorf("%w: %s", ErrMissing, key))
}

func (l *Loader) invalid(key, value string, err error) {
	// The value is quoted so the operator sees what was wrong. Callers never
	// route secrets through here: Secret has no parse step that can fail.
	l.errs = append(l.errs, fmt.Errorf("%w: %s=%q: %v", ErrInvalid, key, value, err))
}

// String returns key's value, or def when unset.
func (l *Loader) String(key, def string) string {
	if v, ok := l.raw(key); ok {
		return v
	}
	return def
}

// RequiredString returns key's value and records ErrMissing when unset.
func (l *Loader) RequiredString(key string) string {
	v, ok := l.raw(key)
	if !ok {
		l.missing(key)
	}
	return v
}

// OneOf returns key's value, or def when unset, and records ErrInvalid when
// the value is not in allowed.
func (l *Loader) OneOf(key, def string, allowed ...string) string {
	v := l.String(key, def)
	if !slices.Contains(allowed, v) {
		l.invalid(key, v, fmt.Errorf("must be one of %s", strings.Join(allowed, ", ")))
	}
	return v
}

// Int returns key parsed as a base-10 integer, or def when unset.
func (l *Loader) Int(key string, def int) int {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.invalid(key, v, errors.New("not an integer"))
		return def
	}
	return n
}

// IntRange is Int with an inclusive bound check.
func (l *Loader) IntRange(key string, def, minimum, maximum int) int {
	n := l.Int(key, def)
	if n < minimum || n > maximum {
		l.invalid(key, strconv.Itoa(n), fmt.Errorf("must be between %d and %d", minimum, maximum))
		return def
	}
	return n
}

// Bool returns key parsed by strconv.ParseBool, or def when unset.
func (l *Loader) Bool(key string, def bool) bool {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.invalid(key, v, errors.New("not a boolean"))
		return def
	}
	return b
}

// Duration returns key parsed by time.ParseDuration ("250ms", "5s"), or def
// when unset. Negative durations are rejected: no setting here means "the
// past".
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		l.invalid(key, v, errors.New("not a non-negative duration"))
		return def
	}
	return d
}

// List returns key split on commas with blanks removed, or def when unset.
func (l *Loader) List(key string, def []string) []string {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	var out []string
	for part := range strings.SplitSeq(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Secret returns key as a Secret and records ErrMissing when unset.
func (l *Loader) Secret(key string) Secret {
	return Secret{value: l.RequiredString(key)}
}

// OptionalSecret returns key as a Secret, or the empty Secret when unset.
func (l *Loader) OptionalSecret(key string) Secret {
	return Secret{value: l.String(key, "")}
}
