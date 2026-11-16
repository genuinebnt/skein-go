package domain

import "errors"

// Errors returned by this package. Test with errors.Is; each is wrapped with
// detail that is safe to show a developer (never note contents).
var (
	// ErrInvalidArgument means a caller-supplied value is malformed. The gRPC
	// adapter maps it to INVALID_ARGUMENT.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrUnknownKind means a notification kind is unspecified or not one
	// this version knows.
	ErrUnknownKind = errors.New("unknown notification kind")
	// ErrDuplicateKind means a preference update lists a kind twice.
	ErrDuplicateKind = errors.New("notification kind listed twice")
	// ErrInvalidNotification means a notification breaks an invariant; it
	// is a bug in a producer or a corrupt row, never user input.
	ErrInvalidNotification = errors.New("invalid notification")
	// ErrInvalidPageToken means a page token is malformed or was issued for
	// different parameters.
	ErrInvalidPageToken = errors.New("invalid page token")
)
