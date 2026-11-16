package edge

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/logging"
)

func hasDetailNamed(st *spb.Status, kind string) bool {
	for _, d := range st.GetDetails() {
		if strings.HasSuffix(d.GetTypeUrl(), kind) {
			return true
		}
	}
	return false
}

func TestPublicStatusHidesServerFaultsButKeepsRetryInfo(t *testing.T) {
	st, _ := status.New(codes.Internal, "pq: password authentication failed for user skein").
		WithDetails(&errdetails.DebugInfo{Detail: "stack"}, &errdetails.RetryInfo{})
	out := PublicStatus(st, "req-1")
	if out.GetMessage() != "Internal Server Error" || hasDetailNamed(out, "DebugInfo") ||
		!hasDetailNamed(out, "RetryInfo") || !hasDetailNamed(out, "RequestInfo") {
		t.Errorf("got %v", out)
	}
	// The original status is not modified.
	if st.Message() == out.GetMessage() || len(st.Details()) != 2 {
		t.Error("PublicStatus changed its input")
	}
}

func TestPublicStatusKeepsAnExistingRequestInfo(t *testing.T) {
	st, _ := status.New(codes.NotFound, "x").WithDetails(&errdetails.RequestInfo{RequestId: "from-backend"})
	out := PublicStatus(st, "req-1")
	n := 0
	for _, d := range out.GetDetails() {
		if strings.HasSuffix(d.GetTypeUrl(), "RequestInfo") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d RequestInfo details", n)
	}
}

func TestStatusOfMapsContextErrors(t *testing.T) {
	if StatusOf(context.Canceled).Code() != codes.Canceled || StatusOf(context.DeadlineExceeded).Code() != codes.DeadlineExceeded ||
		StatusOf(errors.New("boom")).Code() != codes.Unknown {
		t.Error("context errors mapped wrongly")
	}
}

func TestRecoverTurnsAPanicIntoAStatus500AndLogsTheStack(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	h := WithRouteLabel(Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		LabelRoute(r.Context(), r.Method, "/v1/boom")
		panic("bad state")
	})))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/boom", nil)
	h.ServeHTTP(rec, req.WithContext(logging.WithRequestID(req.Context(), "req-9")))
	var st spb.Status
	if err := protojson.Unmarshal(rec.Body.Bytes(), &st); err != nil || rec.Code != 500 || st.GetCode() != int32(codes.Internal) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), `"panic":"bad state"`) || !strings.Contains(logs.String(), `"http.route":"/v1/boom"`) ||
		!strings.Contains(logs.String(), "stack") {
		t.Errorf("log: %s", logs.String())
	}
}

func TestVerifyBearerDistinguishesExpiredFromInvalid(t *testing.T) {
	key, _ := auth.GenerateKey()
	signer := auth.NewSigner(key, func() time.Time { return time.Now().Add(-time.Hour) })
	verifier := auth.NewVerifier(auth.NewKeySet(signer.PublicKey()), auth.VerifierOptions{})
	expired, _, _ := signer.Issue(auth.Subject{UserID: "u"}, time.Minute)
	for token, reason := range map[string]string{expired: ReasonTokenExpired, "": ReasonTokenInvalid, "x.y.z": ReasonTokenInvalid} {
		_, _, err := VerifyBearer(context.Background(), verifier, token)
		st := status.Convert(err)
		var info *errdetails.ErrorInfo
		for _, d := range st.Details() {
			if i, ok := d.(*errdetails.ErrorInfo); ok {
				info = i
			}
		}
		if st.Code() != codes.Unauthenticated || info == nil || info.GetReason() != reason {
			t.Errorf("token %q: %v", token, st)
		}
	}
}
