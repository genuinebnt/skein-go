package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateIdempotencyKey(t *testing.T) {
	for _, ok := range []string{"6f1c2e8a-3b4d-4c5e-9f60-718293a4b5c6", "a", strings.Repeat("k", 255), "~!@#"} {
		if err := ValidateIdempotencyKey(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("k", 256), "has space", "tab\t", "é", "nul\x00"} {
		if err := ValidateIdempotencyKey(bad); !errors.Is(err, ErrInvalidIdempotencyKey) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestFingerprintSeparatesFieldsAndCoversEachOne(t *testing.T) {
	base := Fingerprint("POST", "/v1/a", []byte("{}"))
	if base != Fingerprint("POST", "/v1/a", []byte("{}")) {
		t.Fatal("fingerprint is not deterministic")
	}
	differs := []string{
		Fingerprint("PUT", "/v1/a", []byte("{}")),
		Fingerprint("POST", "/v1/b", []byte("{}")),
		Fingerprint("POST", "/v1/a?x=1", []byte("{}")),
		Fingerprint("POST", "/v1/a", []byte(`{"x":1}`)),
		// Moving a byte across a field boundary must change the hash.
		Fingerprint("POST/", "v1/a", []byte("{}")),
		Fingerprint("POST", "/v1/a{", []byte("}")),
	}
	for i, d := range differs {
		if d == base {
			t.Errorf("variant %d has the same fingerprint", i)
		}
	}
}

func TestDecide(t *testing.T) {
	pending := IdempotencyRecord{State: RecordPending, Fingerprint: "f"}
	complete := IdempotencyRecord{State: RecordComplete, Fingerprint: "f"}
	cases := []struct {
		stored      IdempotencyRecord
		fingerprint string
		want        Outcome
	}{
		{complete, "f", OutcomeReplay},
		{pending, "f", OutcomeInFlight},
		{complete, "g", OutcomeMismatch},
		{pending, "g", OutcomeMismatch},
	}
	for _, c := range cases {
		if got := Decide(c.stored, c.fingerprint); got != c.want {
			t.Errorf("Decide(%s, %s) = %s, want %s", c.stored.State, c.fingerprint, got, c.want)
		}
	}
}

func TestOnlyResponsesARetryWouldRepeatAreStorable(t *testing.T) {
	for _, s := range []int{200, 201, 204, 400, 403, 404, 422} {
		if !Storable(s) {
			t.Errorf("%d should be stored", s)
		}
	}
	for _, s := range []int{401, 409, 429, 499, 500, 503, 504} {
		if Storable(s) {
			t.Errorf("%d should be released for a real retry", s)
		}
	}
}
