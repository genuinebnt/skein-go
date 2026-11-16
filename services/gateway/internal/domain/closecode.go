package domain

import (
	"unicode/utf8"

	"google.golang.org/grpc/codes"
)

// CloseCode is a WebSocket close status code (RFC 6455 section 7.4).
type CloseCode uint16

// Close codes from docs/api/websocket.md.
const (
	CloseNormal          CloseCode = 1000
	CloseGoingAway       CloseCode = 1001
	CloseMessageTooBig   CloseCode = 1009
	CloseInternalError   CloseCode = 1011
	CloseServiceRestart  CloseCode = 1012
	CloseTryAgainLater   CloseCode = 1013
	CloseBadRequest      CloseCode = 4400
	CloseUnauthenticated CloseCode = 4401
	CloseForbidden       CloseCode = 4403
	CloseNotFound        CloseCode = 4404
	CloseRateLimited     CloseCode = 4429
)

// String is the code's short name, used as a metric attribute.
func (c CloseCode) String() string {
	switch c {
	case CloseNormal:
		return "normal"
	case CloseGoingAway:
		return "going_away"
	case CloseMessageTooBig:
		return "message_too_big"
	case CloseInternalError:
		return "internal_error"
	case CloseServiceRestart:
		return "service_restart"
	case CloseTryAgainLater:
		return "try_again_later"
	case CloseBadRequest:
		return "bad_request"
	case CloseUnauthenticated:
		return "unauthenticated"
	case CloseForbidden:
		return "forbidden"
	case CloseNotFound:
		return "not_found"
	case CloseRateLimited:
		return "rate_limited"
	}
	return "other"
}

// CloseCodeForStatus maps the gRPC status that ended a stream to the close
// code the client sees (see the package documentation for codes the
// documented table leaves out).
func CloseCodeForStatus(c codes.Code) CloseCode {
	switch c {
	case codes.OK:
		return CloseNormal
	case codes.Canceled:
		return CloseGoingAway
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange, codes.AlreadyExists:
		return CloseBadRequest
	case codes.Unauthenticated:
		return CloseUnauthenticated
	case codes.PermissionDenied:
		return CloseForbidden
	case codes.NotFound:
		return CloseNotFound
	case codes.ResourceExhausted:
		return CloseRateLimited
	case codes.Unavailable:
		return CloseServiceRestart
	}
	// Internal, Unknown, DataLoss, Unimplemented, Aborted, DeadlineExceeded.
	return CloseInternalError
}

// MaxCloseReasonBytes is the longest close reason a control frame can carry
// (125 bytes of payload minus the 2-byte code).
const MaxCloseReasonBytes = 123

// CloseReason truncates s to MaxCloseReasonBytes without splitting a UTF-8
// sequence, since a close reason must be valid UTF-8.
func CloseReason(s string) string {
	if len(s) <= MaxCloseReasonBytes {
		return s
	}
	cut := MaxCloseReasonBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
