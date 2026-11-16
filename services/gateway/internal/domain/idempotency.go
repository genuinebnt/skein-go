package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
)

// MaxIdempotencyKeyLength is the longest accepted Idempotency-Key.
const MaxIdempotencyKeyLength = 255

// ErrInvalidIdempotencyKey means a key is empty, too long or contains
// characters other than printable ASCII without spaces.
var ErrInvalidIdempotencyKey = errors.New("domain: invalid idempotency key")

// ValidateIdempotencyKey checks a client's Idempotency-Key. Printable ASCII
// is required because the key ends up in a Redis key and in logs.
func ValidateIdempotencyKey(key string) error {
	if key == "" || len(key) > MaxIdempotencyKeyLength {
		return fmt.Errorf("%w: must be 1 to %d characters", ErrInvalidIdempotencyKey, MaxIdempotencyKeyLength)
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return fmt.Errorf("%w: only printable ASCII without spaces is allowed", ErrInvalidIdempotencyKey)
		}
	}
	return nil
}

// Fingerprint identifies a request for idempotency: SHA-256 over the
// method, the path with its query, and the body, each prefixed with its
// length so that bytes cannot move from one field to the next. Hex
// encoded.
func Fingerprint(method, pathAndQuery string, body []byte) string {
	h := sha256.New()
	for _, field := range [][]byte{[]byte(method), []byte(pathAndQuery), body} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(field)))
		h.Write(n[:])
		h.Write(field)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RecordState is the kind of an idempotency record.
type RecordState string

// The two states.
const (
	RecordPending  RecordState = "pending"
	RecordComplete RecordState = "complete"
)

// StoredResponse is the part of a response that a replay repeats.
type StoredResponse struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Body        []byte `json:"body,omitempty"`
}

// IdempotencyRecord is what the gateway keeps per caller and key.
type IdempotencyRecord struct {
	State       RecordState `json:"state"`
	Fingerprint string      `json:"fingerprint"`
	// Owner identifies the request holding a pending record, so that only
	// it can complete or release the record, never a later request that
	// reserved the key after the first reservation expired.
	Owner string `json:"owner,omitempty"`
	// Response is set only when State is RecordComplete.
	Response StoredResponse `json:"response,omitzero"`
}

// Outcome is what to do with a request that carries an idempotency key.
type Outcome string

// The four outcomes.
const (
	// OutcomeProceed: run the request; nothing is stored for the key.
	OutcomeProceed Outcome = "proceed"
	// OutcomeReplay: send the stored response again.
	OutcomeReplay Outcome = "replay"
	// OutcomeMismatch: the key was used for a different request
	// (INVALID_ARGUMENT).
	OutcomeMismatch Outcome = "mismatch"
	// OutcomeInFlight: the first request with this key is still running
	// (ABORTED; retry shortly).
	OutcomeInFlight Outcome = "in_flight"
)

// Decide judges a retry with fingerprint against the stored record.
func Decide(stored IdempotencyRecord, fingerprint string) Outcome {
	switch {
	case stored.Fingerprint != fingerprint:
		return OutcomeMismatch
	case stored.State == RecordComplete:
		return OutcomeReplay
	default:
		return OutcomeInFlight
	}
}

// Storable reports whether a response with this HTTP status is kept for
// replay (see the package documentation).
func Storable(status int) bool {
	switch {
	case status >= 200 && status < 300:
		return true
	case status == http.StatusBadRequest, status == http.StatusForbidden,
		status == http.StatusNotFound, status == http.StatusUnprocessableEntity:
		return true
	}
	return false
}
