package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestDefaultsApplyWhenUnsetOrEmpty(t *testing.T) {
	l := FromMap(map[string]string{"EMPTY": "  "})
	if got := l.String("EMPTY", "def"); got != "def" {
		t.Errorf("empty variable: got %q, want default", got)
	}
	if got := l.Int("UNSET", 7); got != 7 {
		t.Errorf("unset int: got %d", got)
	}
	if got := l.Duration("UNSET", time.Second); got != time.Second {
		t.Errorf("unset duration: got %v", got)
	}
	if err := l.Err(); err != nil {
		t.Fatalf("defaults must not record errors: %v", err)
	}
}

func TestParsesTypedValues(t *testing.T) {
	l := FromMap(map[string]string{
		"PORT": "8080", "DEBUG": "true", "TIMEOUT": "250ms",
		"ORIGINS": " https://a.example, ,https://b.example ", "MODE": "fast",
	})
	if got := l.Int("PORT", 0); got != 8080 {
		t.Errorf("PORT = %d", got)
	}
	if !l.Bool("DEBUG", false) {
		t.Error("DEBUG = false")
	}
	if got := l.Duration("TIMEOUT", 0); got != 250*time.Millisecond {
		t.Errorf("TIMEOUT = %v", got)
	}
	if got := l.List("ORIGINS", nil); strings.Join(got, "|") != "https://a.example|https://b.example" {
		t.Errorf("ORIGINS = %q", got)
	}
	if got := l.OneOf("MODE", "slow", "slow", "fast"); got != "fast" {
		t.Errorf("MODE = %q", got)
	}
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestErrReportsEveryProblemAtOnce(t *testing.T) {
	l := FromMap(map[string]string{"PORT": "eighty", "TIMEOUT": "-1s", "MODE": "warp", "N": "99"})
	l.Int("PORT", 0)
	l.Duration("TIMEOUT", 0)
	l.OneOf("MODE", "slow", "slow", "fast")
	l.IntRange("N", 1, 1, 10)
	l.RequiredString("DATABASE_URL")
	l.Secret("SIGNING_KEY")

	err := l.Err()
	if !errors.Is(err, ErrInvalid) || !errors.Is(err, ErrMissing) {
		t.Fatalf("want both ErrInvalid and ErrMissing, got %v", err)
	}
	for _, key := range []string{"PORT", "TIMEOUT", "MODE", "N", "DATABASE_URL", "SIGNING_KEY"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
}

func TestInvalidValueFallsBackToDefault(t *testing.T) {
	l := FromMap(map[string]string{"PORT": "x"})
	if got := l.Int("PORT", 9090); got != 9090 {
		t.Errorf("got %d, want the default after a parse error", got)
	}
}

func TestSecretIsRedactedEverywhere(t *testing.T) {
	const plaintext = "hunter2-very-secret"
	s := NewSecret(plaintext)

	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("x", "key", s)
	jsonBytes, _ := json.Marshal(struct{ Key Secret }{s})
	textBytes, _ := s.MarshalText()

	outputs := map[string]string{
		"%v":   fmt.Sprintf("%v", s),
		"%s":   fmt.Sprintf("%s", s),
		"%+v":  fmt.Sprintf("%+v", struct{ K Secret }{s}),
		"%#v":  fmt.Sprintf("%#v", s),
		"%x":   fmt.Sprintf("%x", s),
		"%q":   fmt.Sprintf("%q", s),
		"slog": logBuf.String(),
		"json": string(jsonBytes),
		"text": string(textBytes),
	}
	for form, out := range outputs {
		if strings.Contains(out, plaintext) || strings.Contains(out, fmt.Sprintf("%x", plaintext)) {
			t.Errorf("%s leaks the secret: %s", form, out)
		}
		if !strings.Contains(out, redacted) {
			t.Errorf("%s does not say it was redacted: %s", form, out)
		}
	}
	if s.Reveal() != plaintext {
		t.Error("Reveal must return the plaintext")
	}
}

func TestLoadServiceValidatesEnv(t *testing.T) {
	l := FromMap(map[string]string{"SKEIN_ENV": "production"})
	LoadService(l, "identity")
	if !errors.Is(l.Err(), ErrInvalid) {
		t.Fatalf("unknown SKEIN_ENV must be rejected, got %v", l.Err())
	}

	l = FromMap(map[string]string{"SKEIN_VERSION": "1.2.3"})
	svc := LoadService(l, "identity")
	if svc.Env != EnvDev || svc.Version != "1.2.3" || svc.Name != "identity" {
		t.Errorf("got %+v", svc)
	}
	if !svc.Env.IsDev() || EnvProd.IsDev() {
		t.Error("IsDev is wrong")
	}
}
