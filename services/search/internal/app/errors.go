package app

import (
	"errors"
	"fmt"
)

// Error classes of the use cases. The gRPC adapter maps them to status
// codes with error details; nothing else in the service decides codes.
var (
	ErrInvalidArgument = errors.New("invalid argument")
	ErrNotFound        = errors.New("not found")
	ErrUnavailable     = errors.New("unavailable")
	// ErrUnprocessable marks an event that can never be applied (a payload
	// that does not decode). The consumer dead-letters it.
	ErrUnprocessable = errors.New("unprocessable event")
)

// FieldError is an invalid request field, reported as a BadRequest field
// violation.
type FieldError struct {
	Field, Description string
}

func (e *FieldError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Description) }

// Unwrap makes FieldError an ErrInvalidArgument.
func (e *FieldError) Unwrap() error { return ErrInvalidArgument }

func invalid(field, format string, args ...any) error {
	return &FieldError{Field: field, Description: fmt.Sprintf(format, args...)}
}

// NotFoundError names a resource the caller cannot see, reported as
// ResourceInfo. Unreadable notes are NOT_FOUND, never PERMISSION_DENIED, so
// IDs cannot be probed.
type NotFoundError struct {
	ResourceType, Name string
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("%s %s not found", e.ResourceType, e.Name) }

// Unwrap makes NotFoundError an ErrNotFound.
func (e *NotFoundError) Unwrap() error { return ErrNotFound }

func noteNotFound(id string) error { return &NotFoundError{ResourceType: "skein.v1.Note", Name: id} }

// IsNotFound reports whether err is a NOT_FOUND condition.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
