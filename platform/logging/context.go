package logging

import (
	"context"

	"github.com/google/uuid"
)

type contextKey int

const (
	requestIDKey contextKey = iota
	subjectKey
)

type subject struct {
	userID     string
	agentRunID string
}

// WithRequestID returns ctx carrying id as the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID returns the request ID in ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithSubject returns ctx whose records carry user_id and, for a delegated
// agent token, agent_run_id. The auth interceptors call it after verifying a
// token; nothing else should.
func WithSubject(ctx context.Context, userID, agentRunID string) context.Context {
	return context.WithValue(ctx, subjectKey, subject{userID: userID, agentRunID: agentRunID})
}

// NewRequestID returns a fresh UUIDv7 request ID.
func NewRequestID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 only fails when the system random source fails, at which
		// point a random v4 (which panics on the same failure) is as good
		// as anything.
		return uuid.NewString()
	}
	return id.String()
}

// maxRequestIDLen bounds client-supplied IDs; a UUID is 36 characters, and
// the slack allows trace-style IDs from other systems.
const maxRequestIDLen = 128

// ValidRequestID reports whether a client-supplied request ID is safe to
// log and propagate: 1 to 128 characters from [A-Za-z0-9._:-].
func ValidRequestID(id string) bool {
	if len(id) == 0 || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

// RequestIDOrNew returns incoming when it is valid, otherwise a new ID.
func RequestIDOrNew(incoming string) string {
	if ValidRequestID(incoming) {
		return incoming
	}
	return NewRequestID()
}
