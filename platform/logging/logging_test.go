package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/genuinebnt/skein-go/platform/config"
)

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func newTestLogger(buf *bytes.Buffer, extra ...slog.Handler) *slog.Logger {
	return New(Options{Service: "identity", Version: "1.0.0", Env: "test", Writer: buf, Extra: extra})
}

func TestRecordsCarryServiceAndContextFields(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	ctx = WithRequestID(ctx, "req-1")
	ctx = WithSubject(ctx, "user-1", "run-1")
	log.InfoContext(ctx, "hello", "n", 3)

	got := decodeLines(t, &buf)[0]
	want := map[string]any{
		"service": "identity", "version": "1.0.0", "env": "test",
		"trace_id": traceID.String(), "span_id": spanID.String(),
		"request_id": "req-1", "user_id": "user-1", "agent_run_id": "run-1",
		"msg": "hello", "n": float64(3),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestRecordsWithoutContextOmitContextFields(t *testing.T) {
	var buf bytes.Buffer
	newTestLogger(&buf).InfoContext(context.Background(), "plain")
	got := decodeLines(t, &buf)[0]
	for _, k := range []string{"trace_id", "span_id", "request_id", "user_id", "agent_run_id"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s present without a value in context", k)
		}
	}
}

func TestSensitiveKeysAreRedactedAtEveryLevel(t *testing.T) {
	var buf bytes.Buffer
	var bridged bytes.Buffer
	extra := slog.NewJSONHandler(&bridged, nil)
	log := newTestLogger(&buf, extra).With("session_token", "tok-with")

	log.Info("login",
		"password", "pw-plain",
		"Authorization", "Bearer abc",
		"accessToken", "tok-camel",
		slog.Group("req", slog.String("x-api-key", "key-1"), slog.String("path", "/v1/notes")),
		"secret_value", config.NewSecret("sec-1"),
		"input_tokens", 1234,
	)

	for name, out := range map[string]string{"json": buf.String(), "extra sink": bridged.String()} {
		for _, leaked := range []string{"pw-plain", "Bearer abc", "tok-camel", "tok-with", "key-1", "sec-1"} {
			if strings.Contains(out, leaked) {
				t.Errorf("%s leaks %q: %s", name, leaked, out)
			}
		}
		if !strings.Contains(out, "/v1/notes") || !strings.Contains(out, "1234") {
			t.Errorf("%s over-redacted non-sensitive fields: %s", name, out)
		}
	}
}

func TestIsSensitiveKeyMatchesWholeWords(t *testing.T) {
	cases := map[string]bool{
		"password": true, "PASSWORD": true, "user_password": true,
		"access_token": true, "refreshToken": true, "Authorization": true,
		"set-cookie": true, "X-Api-Key": true, "api_key": true, "apiKey": true,
		"client_secret": true, "private_key_pem": true, "jwt": true,
		"input_tokens": false, "tokenizer": false, "path": false,
		"author": false, "user_id": false, "secretary": false,
	}
	for key, want := range cases {
		if got := IsSensitiveKey(key); got != want {
			t.Errorf("IsSensitiveKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestLevelFiltersRecords(t *testing.T) {
	var buf bytes.Buffer
	log := New(Options{Writer: &buf, Level: slog.LevelWarn})
	log.Info("dropped")
	log.Warn("kept")
	if strings.Contains(buf.String(), "dropped") || !strings.Contains(buf.String(), "kept") {
		t.Errorf("level filter wrong: %s", buf.String())
	}
}

func TestRequestIDValidation(t *testing.T) {
	valid := []string{"a", "req-123", "4bf92f35:00f0.ab_C", strings.Repeat("x", 128)}
	invalid := []string{"", strings.Repeat("x", 129), "has space", "new\nline", "quote\"", "é"}
	for _, id := range valid {
		if !ValidRequestID(id) {
			t.Errorf("%q should be valid", id)
		}
		if RequestIDOrNew(id) != id {
			t.Errorf("valid %q was replaced", id)
		}
	}
	for _, id := range invalid {
		if ValidRequestID(id) {
			t.Errorf("%q should be invalid", id)
		}
		if got := RequestIDOrNew(id); got == id || !ValidRequestID(got) {
			t.Errorf("invalid %q replaced by %q", id, got)
		}
	}
	if a, b := NewRequestID(), NewRequestID(); a == b || !ValidRequestID(a) {
		t.Errorf("NewRequestID: %q %q", a, b)
	}
}

// FuzzRequestIDOrNew checks that whatever a client sends, the ID that
// reaches the logs is always safe.
func FuzzRequestIDOrNew(f *testing.F) {
	for _, s := range []string{"", "req-1", "a\nb", strings.Repeat("z", 200), "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, incoming string) {
		got := RequestIDOrNew(incoming)
		if !ValidRequestID(got) {
			t.Fatalf("RequestIDOrNew(%q) = %q is not valid", incoming, got)
		}
		if ValidRequestID(incoming) && got != incoming {
			t.Fatalf("valid ID %q was replaced", incoming)
		}
	})
}

// FuzzIsSensitiveKey checks that normalisation never panics, that the
// decision depends only on the normalised key (so spelling a key with other
// separators or case cannot bypass it), and that a sensitive word anywhere
// is caught.
func FuzzIsSensitiveKey(f *testing.F) {
	for _, s := range []string{"password", "x-api-key", "AccessToken", "a.b.c", "ÄÖ", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, key string) {
		normalised := normaliseKey(key)
		if IsSensitiveKey(key) != IsSensitiveKey(normalised) {
			t.Fatalf("%q and its normal form %q disagree", key, normalised)
		}
		if normaliseKey(normalised) != normalised {
			t.Fatalf("normalisation of %q is not idempotent", key)
		}
		if !IsSensitiveKey(key + "_password") {
			t.Fatalf("%q_password not redacted", key)
		}
	})
}
