package uuid7

import (
	"regexp"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/tools/seed/internal/prng"
)

var v7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestIDsAreVersion7AndSortByTime(t *testing.T) {
	r := prng.New(1).Stream("u", 0)
	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	a, b := New(t1, r), New(t1.Add(time.Millisecond), r)
	if !v7.MatchString(a) || !v7.MatchString(b) {
		t.Fatalf("not UUIDv7: %s %s", a, b)
	}
	if a >= b {
		t.Fatalf("%s should sort before %s", a, b)
	}
	if a[:8] != "019de0d5" {
		t.Fatalf("timestamp prefix %s, want 019de0d5 for 2026-05-01", a[:8])
	}
}
