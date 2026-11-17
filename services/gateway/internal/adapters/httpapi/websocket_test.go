package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/identityevents"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/wsapi"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

const noteID = "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f"

func (s *stack) dial(path string, protocols ...string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(s.url, "http") + path
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: protocols})
	if conn != nil {
		conn.SetReadLimit(4 << 20)
		s.t.Cleanup(func() { _ = conn.CloseNow() })
	}
	return conn, resp, err
}

func (s *stack) mustDial(path string, protocols ...string) *websocket.Conn {
	s.t.Helper()
	conn, resp, err := s.dial(path, protocols...)
	if err != nil {
		body := ""
		if resp != nil {
			body = resp.Status
		}
		s.t.Fatalf("dial %s: %v %s", path, err, body)
	}
	return conn
}

func send(t *testing.T, c *websocket.Conn, binary bool, m proto.Message) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var data []byte
	var err error
	typ := websocket.MessageText
	if binary {
		typ = websocket.MessageBinary
		data, err = proto.Marshal(m)
	} else {
		data, err = protojson.Marshal(m)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, typ, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func receive(t *testing.T, c *websocket.Conn, binary bool, into proto.Message) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if binary != (typ == websocket.MessageBinary) {
		t.Fatalf("got a %s frame", typ)
	}
	if binary {
		err = proto.Unmarshal(data, into)
	} else {
		err = protojson.Unmarshal(data, into)
	}
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// closeOf reads until the connection closes and returns the close code and
// reason the server sent.
func closeOf(t *testing.T, c *websocket.Conn) (websocket.StatusCode, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, _, err := c.Read(ctx)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Code, ce.Reason
		}
		t.Fatalf("connection ended without a close frame: %v", err)
	}
}

func hello(note string) *skeinv1.SyncRequest {
	return &skeinv1.SyncRequest{Message: &skeinv1.SyncRequest_Hello{Hello: &skeinv1.SyncHello{NoteId: note, ReplicaId: 7}}}
}

func ops(b []byte) *skeinv1.SyncRequest {
	return &skeinv1.SyncRequest{Message: &skeinv1.SyncRequest_Ops{Ops: b}}
}

func TestSyncBridgesFramesInBothEncodingsToTheNotesOwner(t *testing.T) {
	s := newStack(t, stackOptions{})
	for _, binary := range []bool{true, false} {
		protocol := wsapi.ProtocolJSON
		if binary {
			protocol = wsapi.ProtocolProto
		}
		conn, resp, err := s.dial("/v1/notes/"+noteID+"/sync", protocol, "bearer."+s.token("ada", time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != protocol {
			t.Errorf("echoed protocol %q, want %q (never the bearer entry)", got, protocol)
		}
		send(t, conn, binary, hello(noteID))
		send(t, conn, binary, ops([]byte{1, 2, 3, 4}))
		var got skeinv1.SyncResponse
		receive(t, conn, binary, &got)
		if !bytes.Equal(got.GetOps(), []byte{1, 2, 3, 4}) {
			t.Errorf("%s: echoed %v", protocol, got.GetOps())
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	owner, _ := s.documents.Owner(noteID)
	calls := s.documents.SyncCalls()
	if len(calls) != 2 {
		t.Fatalf("%d sync streams", len(calls))
	}
	for _, c := range calls {
		if c.Instance != owner {
			t.Errorf("stream went to %s, owner is %s", c.Instance, owner)
		}
		if u := c.MD.Get("x-skein-user-id"); len(u) != 1 || u[0] != "ada" || len(c.MD.Get("authorization")) != 1 || len(c.MD.Get("x-request-id")) != 1 {
			t.Errorf("stream metadata %v", c.MD)
		}
	}
	if n := len(s.backends.CallsTo("/skein.v1.IdentityService/Authorize")); n != 2 {
		t.Errorf("Authorize called %d times before upgrades", n)
	}
}

func TestSyncWithoutAnEncodingDefaultsToJSONAndEchoesNothing(t *testing.T) {
	s := newStack(t, stackOptions{})
	conn, resp, err := s.dial("/v1/notes/"+noteID+"/sync", "bearer."+s.token("ada", time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != "" {
		t.Errorf("echoed %q", got)
	}
	send(t, conn, false, hello(noteID))
	send(t, conn, false, ops([]byte{9}))
	var got skeinv1.SyncResponse
	receive(t, conn, false, &got)
	if !bytes.Equal(got.GetOps(), []byte{9}) {
		t.Errorf("got %v", &got)
	}
}

// Browsers cannot read the HTTP status of a refused upgrade, so refusals of
// a real upgrade are delivered as close codes after the handshake.
func TestUpgradeRefusalsCloseWithTheDocumentedCodes(t *testing.T) {
	limits := generousLimits()
	limits[domain.BudgetKey{Budget: domain.BudgetAPI, Subject: domain.SubjectUser}] = ratelimit.Limit{Rate: 1, Period: time.Hour, Burst: 1}
	s := newStack(t, stackOptions{limits: limits})
	s.backends.DenyNote("secret-note")
	cases := []struct {
		name, path string
		protocols  []string
		code       websocket.StatusCode
		reason     string
	}{
		{"no token", "/v1/notes/" + noteID + "/sync", []string{wsapi.ProtocolProto}, 4401, "missing bearer token"},
		{"bad token", "/v1/notifications/live", []string{wsapi.ProtocolJSON, "bearer.nope"}, 4401, ""},
		{"unreadable note", "/v1/notes/secret-note/sync", []string{wsapi.ProtocolProto, "bearer." + s.token("ada", time.Hour)}, 4404, "note not found"},
		{"over budget", "/v1/notifications/live", []string{wsapi.ProtocolJSON, "bearer." + s.token("ada", time.Hour)}, 4429, ""},
	}
	for _, c := range cases {
		conn, resp, err := s.dial(c.path, c.protocols...)
		if err != nil {
			t.Errorf("%s: upgrade refused at the HTTP level: %v %v", c.name, err, resp)
			continue
		}
		code, reason := closeOf(t, conn)
		if code != c.code || (c.reason != "" && reason != c.reason) {
			t.Errorf("%s: closed with %d %q, want %d %q", c.name, code, reason, c.code, c.reason)
		}
		if code == 4429 {
			if _, err := strconv.Atoi(reason); err != nil {
				t.Errorf("%s: 4429 reason %q is not a number of seconds", c.name, reason)
			}
		}
	}
	if len(s.documents.SyncCalls()) != 0 {
		t.Error("a refused upgrade opened a sync stream")
	}
}

// A request that is not a WebSocket upgrade still gets a google.rpc.Status.
func TestNonUpgradeRequestsToSocketRoutesGetStatusResponses(t *testing.T) {
	s := newStack(t, stackOptions{})
	resp, err := http.Get(s.url + "/v1/notes/" + noteID + "/sync")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	if st := decodeStatus(t, resp); st.GetCode() != int32(codes.Unauthenticated) {
		t.Errorf("status %v", st)
	}
}

func TestSyncRejectsAFirstFrameThatIsNotAHelloForThePathsNote(t *testing.T) {
	s := newStack(t, stackOptions{})
	cases := []struct {
		name  string
		write func(c *websocket.Conn)
	}{
		{"ops first", func(c *websocket.Conn) { send(t, c, true, ops([]byte{1})) }},
		{"hello for another note", func(c *websocket.Conn) { send(t, c, true, hello("other-note")) }},
		{"undecodable", func(c *websocket.Conn) {
			_ = c.Write(context.Background(), websocket.MessageBinary, []byte{0xff, 0xff, 0xff})
		}},
		{"text frame on a proto socket", func(c *websocket.Conn) { _ = c.Write(context.Background(), websocket.MessageText, []byte(`{}`)) }},
	}
	for _, c := range cases {
		conn := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
		c.write(conn)
		if code, _ := closeOf(t, conn); code != 4400 {
			t.Errorf("%s: closed with %d, want 4400", c.name, code)
		}
	}
}

func TestFramesOverOneMebibyteClose1009(t *testing.T) {
	s := newStack(t, stackOptions{})
	conn := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
	send(t, conn, true, hello(noteID))
	_ = conn.Write(context.Background(), websocket.MessageBinary, make([]byte, 1<<20+16))
	if code, _ := closeOf(t, conn); code != websocket.StatusMessageTooBig {
		t.Errorf("closed with %d, want 1009", code)
	}
}

func TestTokenExpiryClosesTheSocketWith4401(t *testing.T) {
	s := newStack(t, stackOptions{})
	conn := s.mustDial("/v1/notifications/live", wsapi.ProtocolJSON, "bearer."+s.token("ada", 2*time.Second))
	start := time.Now()
	code, reason := closeOf(t, conn)
	if code != 4401 || reason != "token expired" {
		t.Errorf("closed with %d %q", code, reason)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("closed after %s", waited)
	}
}

// A client that stops reading fills the bounded queue and is dropped with
// 1013; the documents stream is cancelled rather than blocked.
func TestSlowClientIsDroppedWith1013(t *testing.T) {
	s := newStack(t, stackOptions{ws: wsapi.Options{QueueSize: 4, WriteTimeout: time.Minute}})
	streamEnded := make(chan error, 1)
	s.documents.OnSync(func(_ string, stream skeinv1.DocumentService_SyncServer) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		frame := &skeinv1.SyncResponse{Message: &skeinv1.SyncResponse_Ops{Ops: make([]byte, 64<<10)}}
		for i := 0; ; i++ {
			if err := stream.Send(frame); err != nil {
				streamEnded <- err
				return err
			}
			if i > 100000 {
				streamEnded <- errors.New("never dropped")
				return nil
			}
		}
	})
	conn := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
	send(t, conn, true, hello(noteID))
	select {
	case err := <-streamEnded:
		if status.Code(err) != codes.Canceled && !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("documents stream ended with %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("slow client was never dropped")
	}
	if code, reason := closeOf(t, conn); code != websocket.StatusTryAgainLater {
		t.Errorf("closed with %d %q, want 1013", code, reason)
	}
}

func TestClientsThatStopAnsweringPingsAreClosedWith1001(t *testing.T) {
	s := newStack(t, stackOptions{ws: wsapi.Options{PingInterval: 30 * time.Millisecond, PongTimeout: 60 * time.Millisecond}})
	conn := s.mustDial("/v1/notifications/live", wsapi.ProtocolJSON, "bearer."+s.token("ada", time.Hour))
	// Not reading means pings are never answered.
	time.Sleep(400 * time.Millisecond)
	if code, reason := closeOf(t, conn); code != websocket.StatusGoingAway || reason != "keepalive timeout" {
		t.Errorf("closed with %d %q", code, reason)
	}
}

func TestSyncStreamStatusBecomesTheCloseCode(t *testing.T) {
	s := newStack(t, stackOptions{})
	retry, _ := status.New(codes.ResourceExhausted, "slow down").WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(2500 * time.Millisecond)})
	cases := []struct {
		err    error
		code   websocket.StatusCode
		reason string
	}{
		{nil, 1000, "stream finished"},
		{status.Error(codes.PermissionDenied, "access revoked"), 4403, "access revoked"},
		{status.Error(codes.NotFound, "no such note"), 4404, "no such note"},
		{status.Error(codes.InvalidArgument, "replica not registered"), 4400, "replica not registered"},
		{status.Error(codes.Internal, "pq: deadlock at 10.0.0.7"), 1011, "internal error"},
		{status.Error(codes.Unavailable, "draining"), 1012, "service unavailable"},
		{retry.Err(), 4429, "3"},
	}
	for _, c := range cases {
		s.documents.OnSync(func(string, skeinv1.DocumentService_SyncServer) error { return c.err })
		conn := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
		if code, reason := closeOf(t, conn); code != c.code || reason != c.reason {
			t.Errorf("%v: closed with %d %q, want %d %q", c.err, code, reason, c.code, c.reason)
		}
	}
}

// A client that sends an edit and closes at once (a closed tab) must not
// lose it: the gateway half-closes the documents stream and lets it finish
// instead of cancelling it with the socket (found by make e2e: every such
// edit was lost).
func TestSyncOpsSentJustBeforeTheClientClosesStillReachDocuments(t *testing.T) {
	s := newStack(t, stackOptions{})
	type outcome struct {
		ops [][]byte
		end error
	}
	ended := make(chan outcome, 1)
	s.documents.OnSync(func(_ string, stream skeinv1.DocumentService_SyncServer) error {
		var got outcome
		for {
			// Applying a batch takes a while; the client is long gone.
			time.Sleep(200 * time.Millisecond)
			req, err := stream.Recv()
			if err != nil {
				got.end = err
				ended <- got
				return nil
			}
			if b := req.GetOps(); b != nil {
				got.ops = append(got.ops, b)
			}
		}
	})
	conn := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
	send(t, conn, true, hello(noteID))
	send(t, conn, true, ops([]byte{1, 2, 3}))
	if err := conn.Close(websocket.StatusNormalClosure, "tab closed"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ended:
		if len(got.ops) != 1 || !bytes.Equal(got.ops[0], []byte{1, 2, 3}) {
			t.Errorf("documents received ops %v, want the last batch", got.ops)
		}
		if !errors.Is(got.end, io.EOF) {
			t.Errorf("the stream ended with %v, want a half-close (EOF), not a cancellation", got.end)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the documents stream never ended")
	}
}

func TestSyncFramesOverBudgetClose4429WithTheWait(t *testing.T) {
	limits := generousLimits()
	limits[domain.BudgetKey{Budget: domain.BudgetSync, Subject: domain.SubjectUser}] = ratelimit.PerMinute(30, 2)
	s := newStack(t, stackOptions{limits: limits})
	conn := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
	send(t, conn, true, hello(noteID))
	send(t, conn, true, ops([]byte{1}))
	send(t, conn, true, ops([]byte{2}))
	if code, reason := closeOf(t, conn); code != 4429 || reason != "2" {
		t.Errorf("closed with %d %q, want 4429 \"2\"", code, reason)
	}
}

func TestAgentFramesStartRunsAndRelayTheirEvents(t *testing.T) {
	s := newStack(t, stackOptions{})
	conn := s.mustDial("/v1/chats/chat-1/messages", wsapi.ProtocolJSON, "bearer."+s.token("ada", time.Hour))
	send(t, conn, false, &skeinv1.SendMessageRequest{Text: "hi"})
	var delta, finished skeinv1.SendMessageResponse
	receive(t, conn, false, &delta)
	receive(t, conn, false, &finished)
	if delta.GetTextDelta() != "echo: hi" || delta.GetRunId() != "run-hi" || finished.GetFinished().GetOutcome() != skeinv1.RunFinished_OUTCOME_COMPLETED {
		t.Errorf("events %v %v", &delta, &finished)
	}
	calls := s.backends.CallsTo("/skein.v1.AgentService/SendMessage")
	if len(calls) != 1 || calls[0].MD.Get("x-skein-user-id")[0] != "ada" {
		t.Fatalf("calls %v", calls)
	}
	// The socket stays open for the next run; a frame for another chat
	// is refused.
	send(t, conn, false, &skeinv1.SendMessageRequest{ChatId: "chat-2", Text: "x"})
	if code, _ := closeOf(t, conn); code != 4400 {
		t.Errorf("mismatched chat closed with %d", code)
	}
}

func TestAgentFramesOverBudgetClose4429(t *testing.T) {
	limits := generousLimits()
	limits[domain.BudgetKey{Budget: domain.BudgetAgent, Subject: domain.SubjectUser}] = ratelimit.PerMinute(6, 1)
	s := newStack(t, stackOptions{limits: limits})
	conn := s.mustDial("/v1/chats/chat-1/messages", wsapi.ProtocolJSON, "bearer."+s.token("ada", time.Hour))
	send(t, conn, false, &skeinv1.SendMessageRequest{Text: "one"})
	send(t, conn, false, &skeinv1.SendMessageRequest{Text: "two"})
	if code, reason := closeOf(t, conn); code != 4429 || reason != "10" {
		t.Errorf("closed with %d %q, want 4429 \"10\"", code, reason)
	}
}

func TestClosingTheSocketDoesNotCancelARun(t *testing.T) {
	s := newStack(t, stackOptions{})
	started, release := make(chan struct{}), make(chan struct{})
	var sentAfterClose atomic.Bool
	finished := make(chan struct{})
	s.backends.OnSendMessage(func(_ *skeinv1.SendMessageRequest, stream skeinv1.AgentService_SendMessageServer) error {
		defer close(finished)
		close(started)
		<-release
		if stream.Context().Err() != nil {
			return stream.Context().Err()
		}
		err := stream.Send(&skeinv1.SendMessageResponse{RunId: "r", Event: &skeinv1.SendMessageResponse_TextDelta{TextDelta: "late"}})
		sentAfterClose.Store(err == nil)
		return err
	})
	conn := s.mustDial("/v1/chats/chat-1/messages", wsapi.ProtocolJSON, "bearer."+s.token("ada", time.Hour))
	send(t, conn, false, &skeinv1.SendMessageRequest{Text: "long task"})
	<-started
	_ = conn.Close(websocket.StatusNormalClosure, "navigating away")
	deadline := time.Now().Add(5 * time.Second)
	for s.registry.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	<-finished
	if !sentAfterClose.Load() {
		t.Error("the run was cancelled when its socket closed")
	}
}

func TestNotificationsAreRelayedAndTheStreamEndClosesTheSocket(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.backends.OnSubscribe(func(stream skeinv1.NotificationService_SubscribeServer) error {
		if err := stream.Send(&skeinv1.SubscribeResponse{Notification: &skeinv1.Notification{Id: "n-1", Title: "Grace mentioned you"}}); err != nil {
			return err
		}
		return status.Error(codes.PermissionDenied, "account disabled")
	})
	conn := s.mustDial("/v1/notifications/live", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
	var got skeinv1.SubscribeResponse
	receive(t, conn, true, &got)
	if got.GetNotification().GetId() != "n-1" {
		t.Errorf("got %v", &got)
	}
	if code, _ := closeOf(t, conn); code != 4403 {
		t.Errorf("closed with %d", code)
	}
}

// revokeAccess delivers an identity AccessChanged (revoked) for user and
// noteID through the gateway's real event handler.
func revokeAccess(t *testing.T, s *stack, user, noteID string) {
	t.Helper()
	payload, _ := proto.Marshal(&skeinv1.Event{Id: "e-1", Source: "identity", Payload: &skeinv1.Event_AccessChanged{
		AccessChanged: &skeinv1.AccessChanged{UserId: user, NoteId: noteID, WorkspaceId: "w", Revoked: true}}})
	handler := identityevents.Handler(s.revoke, slog.New(slog.DiscardHandler))
	if err := handler(context.Background(), events.Message{Envelope: events.Envelope{ID: "e-1", Source: "identity", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
}

func TestAccessRevokedEventClosesTheUsersSyncSocketWith4403(t *testing.T) {
	s := newStack(t, stackOptions{})
	mine := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
	other := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("grace", time.Hour))
	send(t, mine, true, hello(noteID))
	send(t, other, true, hello(noteID))
	s.backends.DenyNote(noteID) // ada lost read access; only her socket is re-checked
	revokeAccess(t, s, "ada", noteID)
	if code, reason := closeOf(t, mine); code != 4403 || reason != "access revoked" {
		t.Errorf("closed with %d %q", code, reason)
	}
	// Grace's socket is untouched and still syncing.
	send(t, other, true, ops([]byte{5}))
	var got skeinv1.SyncResponse
	receive(t, other, true, &got)
	if published := s.bus.Published(); len(published) != 1 || published[0].UserID != "ada" {
		t.Errorf("broadcast %v", published)
	}
}

func TestUserLoweredToViewerKeepsTheSyncSocketOpen(t *testing.T) {
	s := newStack(t, stackOptions{})
	token := s.token("ada", time.Hour)
	conn := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+token)
	send(t, conn, true, hello(noteID))
	before := len(s.backends.CallsTo("/skein.v1.IdentityService/Authorize"))
	// identity sets revoked for a demotion too; ada can still read.
	revokeAccess(t, s, "ada", noteID)
	calls := s.backends.CallsTo("/skein.v1.IdentityService/Authorize")
	if len(calls) != before+1 {
		t.Fatalf("%d re-checks, want 1", len(calls)-before)
	}
	if got := calls[len(calls)-1].MD.Get("authorization"); len(got) != 1 || got[0] != "Bearer "+token {
		t.Error("the re-check was not made with the socket's own token")
	}
	send(t, conn, true, ops([]byte{7}))
	var got skeinv1.SyncResponse
	receive(t, conn, true, &got)
	if s.registry.CountByKind(domain.SocketSync) != 1 {
		t.Error("the viewer's sync socket was closed")
	}
}

func TestAFailedAccessRecheckClosesTheSyncSocketWith1012(t *testing.T) {
	s := newStack(t, stackOptions{})
	conn := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
	send(t, conn, true, hello(noteID))
	s.backends.OnUnary("/skein.v1.IdentityService/Authorize", func(context.Context, proto.Message) (proto.Message, error) {
		return nil, status.Error(codes.Unavailable, "identity is down")
	})
	revokeAccess(t, s, "ada", noteID)
	if code, _ := closeOf(t, conn); code != websocket.StatusServiceRestart {
		t.Errorf("closed with %d, want 1012", code)
	}
}

func TestDocumentsMembershipChangeClosesSocketsWhoseNoteMoved(t *testing.T) {
	s := newStack(t, stackOptions{instances: []string{"documents-a:9090", "documents-b:9090"}})
	conn := s.mustDial("/v1/notes/"+noteID+"/sync", wsapi.ProtocolProto, "bearer."+s.token("ada", time.Hour))
	send(t, conn, true, hello(noteID))
	owner, _ := s.documents.Owner(noteID)
	remaining := "documents-a:9090"
	if owner == remaining {
		remaining = "documents-b:9090"
	}
	s.documents.SetMembers(remaining)
	if n := s.registry.Reroute(s.documents.Owner); n != 1 {
		t.Fatalf("rerouted %d sockets", n)
	}
	if code, _ := closeOf(t, conn); code != websocket.StatusServiceRestart {
		t.Errorf("closed with %d, want 1012", code)
	}
}

// Drain on a real listener: readiness turns 503, in-flight REST finishes,
// sockets close with 1012, and new connections are refused.
func TestDrainFinishesInFlightRequestsAndClosesSocketsWith1012(t *testing.T) {
	s := newStack(t, stackOptions{drain: 300 * time.Millisecond})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.server.Serve(context.Background(), lis) }()
	s.url = "http://" + lis.Addr().String()

	started, release := make(chan struct{}), make(chan struct{})
	s.backends.OnUnary("/skein.v1.DocumentService/GetNote", func(context.Context, proto.Message) (proto.Message, error) {
		close(started)
		<-release
		return &skeinv1.GetNoteResponse{Note: &skeinv1.Note{Id: "n1"}}, nil
	})
	inFlight := make(chan int, 1)
	token := s.token("ada", time.Hour)
	go func() { inFlight <- s.do(request{method: "GET", path: "/v1/notes/n1", token: token}).StatusCode }()
	<-started
	conn := s.mustDial("/v1/notifications/live", wsapi.ProtocolJSON, "bearer."+token)

	drained := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		drained <- s.server.Drain(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for s.do(request{method: "GET", path: "/readyz"}).StatusCode != http.StatusServiceUnavailable {
		if time.Now().After(deadline) {
			t.Fatal("readiness never reported draining")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, reason := closeOf(t, conn); code != websocket.StatusServiceRestart || reason != "gateway restarting" {
		t.Errorf("socket closed with %d %q", code, reason)
	}
	close(release)
	if code := <-inFlight; code != http.StatusOK {
		t.Errorf("in-flight request: %d", code)
	}
	if err := <-drained; err != nil {
		t.Errorf("drain: %v", err)
	}
	if err := <-served; err != nil {
		t.Errorf("serve: %v", err)
	}
	if _, err := http.Get(s.url + "/healthz"); err == nil {
		t.Error("new connections still accepted after drain")
	}
}
