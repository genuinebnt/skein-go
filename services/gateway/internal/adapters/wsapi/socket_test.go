package wsapi

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

func TestParseOfferPicksTheFirstEncodingAndExtractsTheToken(t *testing.T) {
	cases := []struct {
		header      []string
		accept, tok string
		binary      bool
	}{
		{[]string{"skein.v1.proto, bearer.abc"}, ProtocolProto, "abc", true},
		{[]string{"skein.v1.json", "bearer.abc"}, ProtocolJSON, "abc", false},
		{[]string{"bearer.abc"}, "", "abc", false},
		{[]string{"skein.v1.json, skein.v1.proto, bearer.a, bearer.b"}, ProtocolJSON, "a", false},
		{[]string{"chat, skein.v1.proto"}, ProtocolProto, "", true},
		{nil, "", "", false},
	}
	for _, c := range cases {
		o := parseOffer(c.header)
		if o.accept != c.accept || o.token != c.tok || o.enc.binary != c.binary {
			t.Errorf("%v: got %+v", c.header, o)
		}
	}
}

// The token must never be chosen as the subprotocol to echo.
func FuzzParseOffer(f *testing.F) {
	f.Add("skein.v1.proto, bearer.eyJ")
	f.Add("bearer.skein.v1.proto")
	f.Fuzz(func(t *testing.T, header string) {
		o := parseOffer([]string{header})
		if o.accept != "" && o.accept != ProtocolJSON && o.accept != ProtocolProto {
			t.Fatalf("echoes %q", o.accept)
		}
		if strings.HasPrefix(o.accept, bearerPrefix) {
			t.Fatal("echoes the token")
		}
	})
}

func TestEncodingRejectsTheWrongFrameType(t *testing.T) {
	var req skeinv1.SyncRequest
	if err := (encoding{binary: true}).decode(websocket.MessageText, []byte("{}"), &req); err == nil {
		t.Error("text frame accepted on a binary socket")
	}
	if err := (encoding{}).decode(websocket.MessageText, []byte(`{"ops":"AQI=","unknownField":1}`), &req); err != nil || len(req.GetOps()) != 2 {
		t.Errorf("JSON with an unknown field: %v", err)
	}
}

func TestSendClosesWith1013WhenTheQueueIsFull(t *testing.T) {
	s := newSocket(context.Background(), encoding{}, Options{QueueSize: 2}.withDefaults(), slog.New(slog.DiscardHandler))
	msg := &skeinv1.SyncResponse{Message: &skeinv1.SyncResponse_Ops{Ops: []byte{1}}}
	if !s.Send(msg) || !s.Send(msg) {
		t.Fatal("queue refused frames below its size")
	}
	if s.Send(msg) {
		t.Fatal("third frame queued past the bound")
	}
	if code, _ := s.closeState(); code != domain.CloseTryAgainLater {
		t.Errorf("closed with %d", code)
	}
	if s.streams.Err() == nil {
		t.Error("closing did not cancel the socket's streams")
	}
	// The first close wins.
	s.Close(domain.CloseForbidden, "later")
	if code, _ := s.closeState(); code != domain.CloseTryAgainLater {
		t.Errorf("a second Close changed the code to %d", code)
	}
	if s.Send(msg) {
		t.Error("closed socket accepted a frame")
	}
}

func TestCloseReasonsDoNotLeakServerFaults(t *testing.T) {
	s := newSocket(context.Background(), encoding{}, Options{}.withDefaults(), slog.New(slog.DiscardHandler))
	closeForStreamEnd(s, status.Error(codes.DataLoss, "disk /var/lib/pg corrupt"))
	if code, reason := s.closeState(); code != domain.CloseInternalError || reason != "internal error" {
		t.Errorf("closed with %d %q", code, reason)
	}
}
