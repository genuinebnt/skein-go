package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/genuinebnt/skein-go/platform/logging"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// errorDomain is the ErrorInfo domain of identity's errors.
const errorDomain = "identity.skein"

// toStatus maps an app or domain error to a gRPC status with the details
// docs/api/README.md promises: BadRequest for invalid fields, ErrorInfo for
// authentication and permission errors, PreconditionFailure, RetryInfo for
// rate limits, and RequestInfo on every error. Server faults are logged
// here and reach the client as "internal error" only.
func toStatus(ctx context.Context, log *slog.Logger, err error) error {
	if err == nil {
		return nil
	}
	var (
		code    codes.Code
		msg     = err.Error()
		details []protoadapt.MessageV1
	)
	var field *domain.FieldError
	var limited *app.RateLimitError
	switch {
	case errors.As(err, &limited):
		code, msg = codes.ResourceExhausted, "too many sign-in attempts"
		details = append(details,
			&errdetails.QuotaFailure{Violations: []*errdetails.QuotaFailure_Violation{{Subject: "sign-in", Description: "sign-in attempts per account and per IP"}}},
			&errdetails.RetryInfo{RetryDelay: durationpb.New(limited.RetryAfter)})
	case errors.As(err, &field):
		code = codes.InvalidArgument
		details = append(details, &errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: field.Field, Description: field.Reason}}})
	case errors.Is(err, domain.ErrInvalidArgument):
		code = codes.InvalidArgument
	case errors.Is(err, domain.ErrNotFound):
		code, msg = codes.NotFound, "not found"
	case errors.Is(err, domain.ErrAlreadyExists):
		code = codes.AlreadyExists
	case errors.Is(err, domain.ErrUnauthenticated):
		// One message for every cause, so accounts and tokens cannot be
		// probed through error text.
		code, msg = codes.Unauthenticated, "authentication failed"
		details = append(details, &errdetails.ErrorInfo{Reason: "CREDENTIALS_INVALID", Domain: errorDomain})
	case errors.Is(err, domain.ErrPermissionDenied):
		code = codes.PermissionDenied
		details = append(details, &errdetails.ErrorInfo{Reason: "ACTION_NOT_ALLOWED", Domain: errorDomain})
	case errors.Is(err, domain.ErrFailedPrecondition):
		code = codes.FailedPrecondition
		details = append(details, &errdetails.PreconditionFailure{Violations: []*errdetails.PreconditionFailure_Violation{{Type: "STATE", Description: msg}}})
	case errors.Is(err, app.ErrProviderUnavailable):
		code, msg = codes.Unavailable, "the sign-in provider is unavailable; retry"
	case errors.Is(err, context.Canceled):
		code, msg = codes.Canceled, "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		code, msg = codes.DeadlineExceeded, "deadline exceeded"
	default:
		log.ErrorContext(ctx, "identity request failed", "error", err)
		code, msg = codes.Internal, "internal error"
	}
	if id := logging.RequestID(ctx); id != "" {
		details = append(details, &errdetails.RequestInfo{RequestId: id})
	}
	st := status.New(code, msg)
	if withDetails, derr := st.WithDetails(details...); derr == nil {
		st = withDetails
	}
	return st.Err()
}
