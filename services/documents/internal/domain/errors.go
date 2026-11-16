package domain

import "errors"

// Errors of the documents service. The gRPC adapter maps each to a status
// code (docs/api/README.md); messages never contain note contents.
//
// Rust: one `thiserror` enum with a variant per sentinel.
var (
	// ErrInvalidArgument means a malformed request (bad ID, empty title,
	// undecodable operations). gRPC INVALID_ARGUMENT.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotFound means no such note, or one the caller may not read, so IDs
	// cannot be probed. gRPC NOT_FOUND.
	ErrNotFound = errors.New("not found")
	// ErrPermissionDenied means the caller can read the note but may not do
	// this to it, or used a replica registered to someone else. gRPC
	// PERMISSION_DENIED.
	ErrPermissionDenied = errors.New("permission denied")
	// ErrFailedPrecondition means a valid request in the wrong state:
	// editing a trashed note, nothing to undo. gRPC FAILED_PRECONDITION.
	ErrFailedPrecondition = errors.New("failed precondition")
	// ErrOutOfRange means a version past what the note has. gRPC
	// OUT_OF_RANGE.
	ErrOutOfRange = errors.New("out of range")
	// ErrAlreadyExists means a note with that ID exists. gRPC
	// ALREADY_EXISTS.
	ErrAlreadyExists = errors.New("already exists")
	// ErrConflict means another writer changed the note concurrently (a
	// second actor for the same note); retry. gRPC ABORTED.
	ErrConflict = errors.New("conflict")
	// ErrUnavailable means a dependency failed; retry with backoff. gRPC
	// UNAVAILABLE.
	ErrUnavailable = errors.New("unavailable")
	// ErrResourceExhausted means a limit was hit: too many pending
	// operations, or a sync client too slow to keep up. gRPC
	// RESOURCE_EXHAUSTED.
	ErrResourceExhausted = errors.New("resource exhausted")
)
