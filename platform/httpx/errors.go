package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/logging"
)

// StatusClientClosedRequest is nginx's 499: the client went away.
const StatusClientClosedRequest = 499

type codeMapping struct {
	http int
	name string // google.rpc.Code's canonical name, used in JSON bodies
}

// codeTable is the documented mapping; see the package documentation.
var codeTable = map[codes.Code]codeMapping{
	codes.OK:                 {http.StatusOK, "OK"},
	codes.Canceled:           {StatusClientClosedRequest, "CANCELLED"},
	codes.Unknown:            {http.StatusInternalServerError, "UNKNOWN"},
	codes.InvalidArgument:    {http.StatusBadRequest, "INVALID_ARGUMENT"},
	codes.DeadlineExceeded:   {http.StatusGatewayTimeout, "DEADLINE_EXCEEDED"},
	codes.NotFound:           {http.StatusNotFound, "NOT_FOUND"},
	codes.AlreadyExists:      {http.StatusConflict, "ALREADY_EXISTS"},
	codes.PermissionDenied:   {http.StatusForbidden, "PERMISSION_DENIED"},
	codes.ResourceExhausted:  {http.StatusTooManyRequests, "RESOURCE_EXHAUSTED"},
	codes.FailedPrecondition: {http.StatusBadRequest, "FAILED_PRECONDITION"},
	codes.Aborted:            {http.StatusConflict, "ABORTED"},
	codes.OutOfRange:         {http.StatusBadRequest, "OUT_OF_RANGE"},
	codes.Unimplemented:      {http.StatusNotImplemented, "UNIMPLEMENTED"},
	codes.Internal:           {http.StatusInternalServerError, "INTERNAL"},
	codes.Unavailable:        {http.StatusServiceUnavailable, "UNAVAILABLE"},
	codes.DataLoss:           {http.StatusInternalServerError, "DATA_LOSS"},
	codes.Unauthenticated:    {http.StatusUnauthorized, "UNAUTHENTICATED"},
}

// StatusFromCode returns the HTTP status for a gRPC code; 500 for codes
// outside the table.
func StatusFromCode(c codes.Code) int {
	if m, ok := codeTable[c]; ok {
		return m.http
	}
	return http.StatusInternalServerError
}

// CodeName returns google.rpc.Code's canonical name for c ("NOT_FOUND").
func CodeName(c codes.Code) string {
	if m, ok := codeTable[c]; ok {
		return m.name
	}
	return "UNKNOWN"
}

// ErrorBody is the JSON shape of every error response.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one error.
type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// codeOf finds the gRPC code for any error: a status error's own code,
// context errors as Canceled or DeadlineExceeded, everything else Unknown.
func codeOf(err error) *status.Status {
	if st, ok := status.FromError(err); ok {
		return st
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.New(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.New(codes.DeadlineExceeded, err.Error())
	}
	return status.New(codes.Unknown, err.Error())
}

// WriteError writes err as a JSON error response. Server faults (5xx) are
// logged with their real message and sent with a generic one.
func WriteError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	st := codeOf(err)
	httpStatus := StatusFromCode(st.Code())
	message := st.Message()
	if httpStatus >= 500 {
		if log != nil {
			log.ErrorContext(r.Context(), "request failed",
				slog.String("code", CodeName(st.Code())), slog.String("error", st.Message()))
		}
		message = http.StatusText(httpStatus)
	}
	if retryAfter, ok := grpcx.RetryAfter(err); ok {
		w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
	}
	writeJSON(w, httpStatus, ErrorBody{Error: ErrorDetail{
		Code: CodeName(st.Code()), Message: message, RequestID: logging.RequestID(r.Context()),
	}})
}

// writeJSON writes v with status. Encoding errors are ignored: the status
// line is already sent, and the values here always encode.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// retryAfterSeconds rounds up, because Retry-After has whole seconds and
// retrying early would be denied again.
func retryAfterSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(math.Ceil(d.Seconds())), 10)
}
