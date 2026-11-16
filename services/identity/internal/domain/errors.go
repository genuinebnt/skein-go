package domain

import (
	"errors"
	"fmt"
)

// Errors returned by identity's domain and app layers. The gRPC adapter maps
// each to one status code; test with errors.Is.
var (
	// ErrInvalidArgument means a request field is malformed. Wrapped by
	// FieldError, which names the field.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotFound means the resource does not exist, or the caller may not
	// know that it exists.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists means a unique value is taken (an email, a replica ID).
	ErrAlreadyExists = errors.New("already exists")
	// ErrUnauthenticated means credentials are missing, wrong or no longer
	// valid. Messages never say which, so accounts cannot be probed.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrPermissionDenied means the caller is known but not allowed.
	ErrPermissionDenied = errors.New("permission denied")
	// ErrFailedPrecondition means a valid request that the current state
	// forbids, such as removing a workspace's last owner.
	ErrFailedPrecondition = errors.New("failed precondition")
)

// FieldError is an ErrInvalidArgument about one request field.
type FieldError struct {
	Field  string
	Reason string
}

// Error implements error.
func (e *FieldError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Reason) }

// Unwrap makes errors.Is(err, ErrInvalidArgument) true.
func (e *FieldError) Unwrap() error { return ErrInvalidArgument }

// InvalidField returns a FieldError.
func InvalidField(field, reason string) error { return &FieldError{Field: field, Reason: reason} }
