package domain

import (
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
)

// The documented table in docs/api/websocket.md, plus the codes it leaves
// out (package documentation).
func TestCloseCodeForStatusFollowsTheDocumentedTable(t *testing.T) {
	cases := map[codes.Code]CloseCode{
		codes.OK:                 1000,
		codes.Internal:           1011,
		codes.Unknown:            1011,
		codes.DataLoss:           1011,
		codes.Unavailable:        1012,
		codes.InvalidArgument:    4400,
		codes.FailedPrecondition: 4400,
		codes.Unauthenticated:    4401,
		codes.PermissionDenied:   4403,
		codes.NotFound:           4404,
		codes.ResourceExhausted:  4429,
		// Not in the table.
		codes.Canceled:         1001,
		codes.OutOfRange:       4400,
		codes.AlreadyExists:    4400,
		codes.Aborted:          1011,
		codes.DeadlineExceeded: 1011,
		codes.Unimplemented:    1011,
	}
	for c := codes.OK; c <= codes.Unauthenticated; c++ {
		want, ok := cases[c]
		if !ok {
			t.Fatalf("test table misses %s", c)
		}
		if got := CloseCodeForStatus(c); got != want {
			t.Errorf("%s: got %d, want %d", c, got, want)
		}
		if CloseCodeForStatus(c).String() == "other" {
			t.Errorf("%s maps to a code without a name", c)
		}
	}
}

func TestCloseReasonTruncatesOnARuneBoundary(t *testing.T) {
	if got := CloseReason("short"); got != "short" {
		t.Errorf("short reason changed: %q", got)
	}
	long := strings.Repeat("a", 122) + "é" + "tail" // é straddles byte 123
	got := CloseReason(long)
	if len(got) != 122 || !utf8.ValidString(got) {
		t.Errorf("got %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}

func FuzzCloseReason(f *testing.F) {
	f.Add(strings.Repeat("日本語", 50))
	f.Add("ok")
	f.Fuzz(func(t *testing.T, s string) {
		got := CloseReason(s)
		if len(got) > MaxCloseReasonBytes || !strings.HasPrefix(s, got) {
			t.Fatalf("CloseReason(%q) = %q", s, got)
		}
		if utf8.ValidString(s) && !utf8.ValidString(got) {
			t.Fatalf("split a rune: %q", got)
		}
		if len(s) <= MaxCloseReasonBytes && got != s {
			t.Fatalf("shortened a reason that fits: %q", got)
		}
	})
}
