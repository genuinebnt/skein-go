package edge

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/genuinebnt/skein-go/platform/httpx"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// ErrorDomain is google.rpc.ErrorInfo's domain for errors the gateway
// raises itself.
const ErrorDomain = "skein.dev"

// ErrorInfo reasons the gateway uses.
const (
	ReasonTokenExpired        = "TOKEN_EXPIRED"
	ReasonTokenInvalid        = "TOKEN_INVALID"
	ReasonIdempotencyKeyInUse = "IDEMPOTENCY_KEY_IN_USE"
)

// unavailableRetryAfter is the RetryInfo added to UNAVAILABLE errors that
// carry none.
const unavailableRetryAfter = time.Second

// StatusOf converts any error to a status: a gRPC status as is, context
// errors as CANCELLED or DEADLINE_EXCEEDED, anything else UNKNOWN.
func StatusOf(err error) *status.Status {
	var gatewayErr *runtime.HTTPStatusError
	if errors.As(err, &gatewayErr) {
		err = gatewayErr.Err
	}
	if st, ok := status.FromError(err); ok {
		return st
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.New(codes.Canceled, "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.New(codes.DeadlineExceeded, "deadline exceeded")
	}
	return status.New(codes.Unknown, err.Error())
}

// WriteError writes err as a google.rpc.Status (see the package
// documentation). log may be nil.
func WriteError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	st := StatusOf(err)
	httpStatus := httpx.StatusFromCode(st.Code())
	if httpStatus >= 500 && log != nil {
		log.ErrorContext(r.Context(), "request failed",
			slog.String("code", httpx.CodeName(st.Code())), slog.String("error", st.Message()))
	}
	body := PublicStatus(st, logging.RequestID(r.Context()))
	if d, ok := retryDelay(body); ok {
		w.Header().Set("Retry-After", seconds(d))
	}
	if st.Code() == codes.Unauthenticated && w.Header().Get("WWW-Authenticate") == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	data, merr := protojson.Marshal(body)
	if merr != nil {
		// Unreachable with the detail types used here; keep the status line
		// honest anyway.
		data = []byte(`{"code":13,"message":"Internal Server Error"}`)
		httpStatus = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(httpStatus)
	_, _ = w.Write(data)
}

// PublicStatus returns the status a client may see: generic for server
// faults, with RetryInfo where promised and RequestInfo always.
func PublicStatus(st *status.Status, requestID string) *spb.Status {
	out := proto.Clone(st.Proto()).(*spb.Status)
	if httpx.StatusFromCode(st.Code()) >= 500 {
		out.Message = http.StatusText(httpx.StatusFromCode(st.Code()))
		out.Details = keepOnly(out.Details, &errdetails.RetryInfo{})
	}
	if st.Code() == codes.Unavailable && !hasDetail(out.Details, &errdetails.RetryInfo{}) {
		out.Details = appendDetail(out.Details, &errdetails.RetryInfo{RetryDelay: durationpb.New(unavailableRetryAfter)})
	}
	if requestID != "" && !hasDetail(out.Details, &errdetails.RequestInfo{}) {
		out.Details = appendDetail(out.Details, &errdetails.RequestInfo{RequestId: requestID})
	}
	return out
}

func hasDetail(details []*anypb.Any, kind proto.Message) bool {
	for _, d := range details {
		if d.MessageIs(kind) {
			return true
		}
	}
	return false
}

func keepOnly(details []*anypb.Any, kind proto.Message) []*anypb.Any {
	var kept []*anypb.Any
	for _, d := range details {
		if d.MessageIs(kind) {
			kept = append(kept, d)
		}
	}
	return kept
}

func appendDetail(details []*anypb.Any, m proto.Message) []*anypb.Any {
	a, err := anypb.New(m)
	if err != nil {
		return details
	}
	return append(details, a)
}

func retryDelay(st *spb.Status) (time.Duration, bool) {
	for _, d := range st.GetDetails() {
		var info errdetails.RetryInfo
		if d.MessageIs(&info) && d.UnmarshalTo(&info) == nil && info.GetRetryDelay() != nil {
			return info.GetRetryDelay().AsDuration(), true
		}
	}
	return 0, false
}

// seconds rounds up: Retry-After has whole seconds, and retrying early
// would be denied again.
func seconds(d time.Duration) string {
	return strconv.FormatInt(int64(math.Ceil(d.Seconds())), 10)
}

// SecondsUntil formats a wait as whole seconds, rounded up, at least 1.
// It is the 4429 close reason.
func SecondsUntil(d time.Duration) string {
	if d < time.Second {
		d = time.Second
	}
	return seconds(d)
}

func withDetails(code codes.Code, message string, details ...protoadapt.MessageV1) error {
	st := status.New(code, message)
	if withDetails, err := st.WithDetails(details...); err == nil {
		st = withDetails
	}
	return st.Err()
}

// Unauthenticated is the error for a missing, invalid or expired token.
func Unauthenticated(reason, message string) error {
	return withDetails(codes.Unauthenticated, message, &errdetails.ErrorInfo{Reason: reason, Domain: ErrorDomain})
}

// RateLimited is the error for a request over budget: QuotaFailure names
// the budget and subject kind ("search:user"), RetryInfo says how long to
// wait.
func RateLimited(key domain.BudgetKey, d ratelimit.Decision) error {
	return withDetails(codes.ResourceExhausted, "rate limit exceeded for the "+string(key.Budget)+" budget",
		&errdetails.QuotaFailure{Violations: []*errdetails.QuotaFailure_Violation{{
			Subject:     string(key.Budget) + ":" + string(key.Subject),
			Description: "too many " + string(key.Budget) + " requests per " + string(key.Subject),
		}}},
		&errdetails.RetryInfo{RetryDelay: durationpb.New(d.RetryAfter)},
	)
}

// InvalidField is INVALID_ARGUMENT with a BadRequest naming one field.
func InvalidField(field, description string) error {
	return withDetails(codes.InvalidArgument, field+" "+description,
		&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: field, Description: description}}})
}

// Aborted is ABORTED with an ErrorInfo reason.
func Aborted(reason, message string) error {
	return withDetails(codes.Aborted, message, &errdetails.ErrorInfo{Reason: reason, Domain: ErrorDomain})
}

// NotFound is NOT_FOUND with a ResourceInfo.
func NotFound(resourceType, name string) error {
	return withDetails(codes.NotFound, resourceType+" not found",
		&errdetails.ResourceInfo{ResourceType: resourceType, ResourceName: name})
}

// WriteRateLimitHeaders sets the RateLimit-* headers (and Retry-After on a
// denial) for a checked verdict.
func WriteRateLimitHeaders(w http.ResponseWriter, d ratelimit.Decision) {
	httpx.WriteRateLimitHeaders(w, d)
}
