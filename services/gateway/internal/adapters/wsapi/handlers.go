package wsapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/logging"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/edge"
	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// Routes of the three endpoints, as net/http patterns and as route labels.
const (
	SyncPattern          = "GET /v1/notes/{noteId}/sync"
	AgentPattern         = "GET /v1/chats/{chatId}/messages"
	NotificationsPattern = "GET /v1/notifications/live"
)

// Config configures Handlers.
type Config struct {
	Verifier      auth.TokenVerifier
	Throttler     *app.Throttler
	ClientIP      edge.ClientIPFunc
	Registry      *app.Registry
	Sync          app.SyncRouter
	NoteAccess    app.NoteAccess
	Agent         skeinv1.AgentServiceClient
	Notifications skeinv1.NotificationServiceClient
	// OriginPatterns are the hosts of the allowed cross-origin frontends
	// (coder/websocket matches them against Origin); same-origin is always
	// allowed, and clients without Origin (not browsers) are too.
	OriginPatterns []string
	Options        Options
	// RunTimeout bounds how long a detached agent run is still read after
	// its socket closed; default 15 minutes.
	RunTimeout time.Duration
	Logger     *slog.Logger
	Meter      metric.MeterProvider
}

// Handlers serves the WebSocket endpoints.
type Handlers struct {
	cfg Config

	runs     sync.WaitGroup
	runsCtx  context.Context
	stopRuns context.CancelFunc

	active metric.Int64UpDownCounter
	closes metric.Int64Counter
}

// New returns the handlers.
func New(cfg Config) (*Handlers, error) {
	cfg.Options = cfg.Options.withDefaults()
	if cfg.RunTimeout == 0 {
		cfg.RunTimeout = 15 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Meter == nil {
		cfg.Meter = noop.NewMeterProvider()
	}
	meter := cfg.Meter.Meter("github.com/genuinebnt/skein-go/services/gateway")
	active, err := meter.Int64UpDownCounter("skein.gateway.websocket.connections",
		metric.WithUnit("{connection}"), metric.WithDescription("Open WebSocket connections by endpoint"))
	if err != nil {
		return nil, err
	}
	closes, err := meter.Int64Counter("skein.gateway.websocket.closes",
		metric.WithUnit("{connection}"), metric.WithDescription("Closed WebSocket connections by endpoint and close code"))
	if err != nil {
		return nil, err
	}
	runsCtx, stopRuns := context.WithCancel(context.Background())
	return &Handlers{cfg: cfg, runsCtx: runsCtx, stopRuns: stopRuns, active: active, closes: closes}, nil
}

// StopRuns waits for detached agent runs until ctx ends, then cancels the
// rest and waits for them to notice. Call it after the registry drained.
func (h *Handlers) StopRuns(ctx context.Context) error {
	done := make(chan struct{})
	go func() { h.runs.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		h.stopRuns()
		<-done
		return ctx.Err()
	}
}

// Register adds the three endpoints to mux.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc(SyncPattern, h.serveSync)
	mux.HandleFunc(AgentPattern, h.serveAgent)
	mux.HandleFunc(NotificationsPattern, h.serveNotifications)
}

// connection is an accepted socket with what the bridges need.
type connection struct {
	*socket
	ctx    context.Context // values of the upgrade request, no cancellation
	claims auth.Claims
}

// upgrade authenticates, rate-limits, runs prepare and accepts. It writes
// the error response itself and returns false on failure. The returned
// finish must be called when the socket is done.
func (h *Handlers) upgrade(w http.ResponseWriter, r *http.Request, kind domain.SocketKind, route string,
	prepare func(ctx context.Context, claims auth.Claims) (app.SocketInfo, error),
) (*connection, func(), bool) {
	edge.LabelRoute(r.Context(), r.Method, route)
	o := parseOffer(r.Header.Values(protocolHeader))
	ctx, claims, err := edge.VerifyBearer(r.Context(), h.cfg.Verifier, o.token)
	if err != nil {
		h.refuse(w, r, o, err)
		return nil, nil, false
	}
	ip := h.cfg.ClientIP(r)
	verdict := h.cfg.Throttler.Check(ctx, domain.BudgetAPI, claims.Subject.UserID, ip)
	if verdict.Checked {
		edge.WriteRateLimitHeaders(w, verdict.Decision)
	}
	if verdict.Limited() {
		h.refuse(w, r.WithContext(ctx), o, edge.RateLimited(verdict.Key, verdict.Decision))
		return nil, nil, false
	}
	info, err := prepare(ctx, claims)
	if err != nil {
		h.refuse(w, r.WithContext(ctx), o, err)
		return nil, nil, false
	}
	// The request's context ends when the handler returns or the server
	// closes the hijacked connection; the socket keeps its values (request
	// ID, claims, span) and controls its own lifetime.
	base := edge.OutgoingSubject(context.WithoutCancel(ctx))
	sock := newSocket(base, o.enc, h.cfg.Options, h.cfg.Logger)
	remove, err := h.cfg.Registry.Add(base, info, sock)
	if err != nil {
		if errors.Is(err, app.ErrDraining) {
			err = status.Error(codes.Unavailable, "gateway is restarting")
		}
		h.refuse(w, r.WithContext(ctx), o, err)
		return nil, nil, false
	}
	var protocols []string
	if o.accept != "" {
		protocols = []string{o.accept}
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: protocols, OriginPatterns: h.cfg.OriginPatterns})
	if err != nil {
		// Accept has written the response (403 for a foreign origin).
		sock.stop()
		remove()
		return nil, nil, false
	}
	sock.conn = conn
	attrs := metric.WithAttributes(attribute.String("skein.websocket.endpoint", string(kind)))
	h.active.Add(ctx, 1, attrs)
	h.cfg.Logger.InfoContext(ctx, "websocket opened", slog.String("endpoint", string(kind)), slog.String("encoding", o.accept))
	finish := func() {
		remove()
		h.active.Add(ctx, -1, attrs)
	}
	return &connection{socket: sock, ctx: base, claims: claims}, finish, true
}

// spend charges one frame to budget; over budget closes the socket.
func (h *Handlers) spend(c *connection, r *http.Request, budget domain.Budget) bool {
	v := h.cfg.Throttler.Check(c.ctx, budget, c.claims.Subject.UserID, h.cfg.ClientIP(r))
	if v.Limited() {
		c.Close(domain.CloseRateLimited, edge.SecondsUntil(v.Decision.RetryAfter))
		return false
	}
	return true
}

func (h *Handlers) finished(c *connection, kind domain.SocketKind, code domain.CloseCode) {
	h.closes.Add(c.ctx, 1, metric.WithAttributes(
		attribute.String("skein.websocket.endpoint", string(kind)),
		attribute.String("skein.websocket.close_code", code.String())))
	_, reason := c.closeState()
	h.cfg.Logger.InfoContext(c.ctx, "websocket closed", slog.String("endpoint", string(kind)),
		slog.Int("close_code", int(code)), slog.String("close_reason", reason))
}

// closeForStreamEnd closes a socket for the end of its gRPC stream.
func closeForStreamEnd(s *socket, err error) {
	if errors.Is(err, io.EOF) || err == nil {
		s.Close(domain.CloseNormal, "stream finished")
		return
	}
	s.Close(closeFor(err))
}

// closeFor is the close code and reason for a failure: the documented
// mapping from its gRPC status, a generic reason for server faults, and the
// seconds to wait for rate limits.
func closeFor(err error) (domain.CloseCode, string) {
	st := edge.StatusOf(err)
	code := domain.CloseCodeForStatus(st.Code())
	reason := st.Message()
	switch code {
	case domain.CloseInternalError:
		reason = "internal error"
	case domain.CloseServiceRestart:
		reason = "service unavailable"
	case domain.CloseRateLimited:
		wait, ok := grpcx.RetryAfter(err)
		if !ok {
			wait = time.Second
		}
		reason = edge.SecondsUntil(wait)
	}
	return code, reason
}

// refuse reports why a socket cannot be opened. Browsers never see the HTTP
// status of a failed upgrade (the WebSocket API only reports "error" and
// close code 1006), so a real upgrade request is accepted and closed at once
// with the documented close code (4401, 4403, 4404, 4429, ...) and reason.
// Anything else, or an origin the gateway refuses, gets the usual
// google.rpc.Status response.
func (h *Handlers) refuse(w http.ResponseWriter, r *http.Request, o offer, err error) {
	if !isUpgrade(r) {
		edge.WriteError(w, r, h.cfg.Logger, err)
		return
	}
	var protocols []string
	if o.accept != "" {
		protocols = []string{o.accept}
	}
	conn, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: protocols, OriginPatterns: h.cfg.OriginPatterns})
	if acceptErr != nil {
		return // Accept has written the response (403 for a foreign origin).
	}
	code, reason := closeFor(err)
	h.cfg.Logger.InfoContext(r.Context(), "websocket refused",
		slog.Int("close_code", int(code)), slog.String("close_reason", reason))
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.Options.WriteTimeout)
	defer cancel()
	// Close waits for the client's close frame; CloseNow is the fallback
	// when the client never answers.
	done := make(chan struct{})
	go func() { _ = conn.Close(websocket.StatusCode(code), reason); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		_ = conn.CloseNow()
	}
}

// isUpgrade reports whether r asks for a WebSocket.
func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func (h *Handlers) serveSync(w http.ResponseWriter, r *http.Request) {
	noteID := r.PathValue("noteId")
	var route app.SyncRoute
	c, finish, ok := h.upgrade(w, r, domain.SocketSync, "/v1/notes/{note_id}/sync",
		func(ctx context.Context, claims auth.Claims) (app.SocketInfo, error) {
			readable, err := h.cfg.NoteAccess.CanRead(ctx, claims.Subject, noteID)
			if err != nil {
				return app.SocketInfo{}, err
			}
			if !readable {
				// Unreadable notes are NOT_FOUND so IDs cannot be probed.
				return app.SocketInfo{}, edge.NotFound("note", noteID)
			}
			if route, err = h.cfg.Sync.Route(noteID); err != nil {
				return app.SocketInfo{}, err
			}
			return app.SocketInfo{Kind: domain.SocketSync, UserID: claims.Subject.UserID, NoteID: noteID, Owner: route.Owner}, nil
		})
	if !ok {
		return
	}
	defer finish()
	// The stream outlives the socket briefly: a client that sends an edit
	// and closes at once (a closed tab) must not lose it. Cancelling the
	// stream when the socket closes aborted frames documents had not
	// applied yet, so the stream has its own context; syncFrames half-closes
	// it, and drainSync cancels it once documents has finished or
	// StreamDrain after the socket began closing.
	streamCtx, cancelStream := context.WithCancel(context.WithoutCancel(c.streams))
	recvDone := make(chan struct{})
	go h.drainSync(c, noteID, recvDone, cancelStream)
	stream, err := route.Client.Sync(streamCtx)
	if err != nil {
		close(recvDone)
		closeForStreamEnd(c.socket, err)
	}
	code := c.serve(c.claims.ExpiresAt, func() {
		if stream == nil {
			c.discard()
			return
		}
		go func() {
			defer close(recvDone)
			for {
				resp, err := stream.Recv()
				if err != nil {
					closeForStreamEnd(c.socket, err)
					return
				}
				c.Send(resp)
			}
		}()
		h.syncFrames(c, r, noteID, stream)
	})
	h.finished(c, domain.SocketSync, code)
}

// drainSync owns the cancellation of a sync socket's documents stream: once
// the socket starts closing, the stream gets StreamDrain to end by itself
// (documents ends it on the half-close, after applying what it received).
// The clock starts at the close, not when the socket's goroutines finish,
// because a client that stops reading can hold the closing handshake for
// seconds.
func (h *Handlers) drainSync(c *connection, noteID string, recvDone <-chan struct{}, cancel context.CancelFunc) {
	defer cancel()
	select {
	case <-recvDone:
		return
	case <-c.closing:
	}
	select {
	case <-recvDone:
	case <-time.After(h.cfg.Options.StreamDrain):
		h.cfg.Logger.WarnContext(c.ctx, "sync stream did not finish after the socket closed; cancelling",
			slog.String("note_id", noteID))
	}
}

// syncFrames forwards client frames to the Sync stream.
func (h *Handlers) syncFrames(c *connection, r *http.Request, noteID string, stream skeinv1.DocumentService_SyncClient) {
	defer func() { _ = stream.CloseSend() }()
	first := true
	for {
		var req skeinv1.SyncRequest
		if err := c.read(&req); err != nil {
			return
		}
		if first {
			hello := req.GetHello()
			if hello == nil || hello.GetNoteId() != noteID {
				c.Close(domain.CloseBadRequest, "the first frame must be a hello for this note")
				c.discard()
				return
			}
			first = false
		}
		if !h.spend(c, r, domain.BudgetSync) {
			c.discard()
			return
		}
		if err := stream.Send(&req); err != nil {
			// The stream ended; Recv reports its status.
			c.discard()
			return
		}
	}
}

func (h *Handlers) serveAgent(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	c, finish, ok := h.upgrade(w, r, domain.SocketAgent, "/v1/chats/{chat_id}/messages",
		func(_ context.Context, claims auth.Claims) (app.SocketInfo, error) {
			return app.SocketInfo{Kind: domain.SocketAgent, UserID: claims.Subject.UserID}, nil
		})
	if !ok {
		return
	}
	defer finish()
	code := c.serve(c.claims.ExpiresAt, func() {
		for {
			var req skeinv1.SendMessageRequest
			if err := c.read(&req); err != nil {
				return
			}
			if req.GetChatId() == "" {
				req.ChatId = chatID
			}
			if req.GetChatId() != chatID {
				c.Close(domain.CloseBadRequest, "chatId in the frame must match the path")
				c.discard()
				return
			}
			if !h.spend(c, r, domain.BudgetAgent) {
				c.discard()
				return
			}
			h.startRun(c, &req)
		}
	})
	h.finished(c, domain.SocketAgent, code)
}

// startRun runs one SendMessage call detached from the socket (see the
// package documentation).
func (h *Handlers) startRun(c *connection, req *skeinv1.SendMessageRequest) {
	h.runs.Add(1)
	go func() {
		defer h.runs.Done()
		ctx, cancel := context.WithTimeout(logging.WithRequestID(c.ctx, logging.NewRequestID()), h.cfg.RunTimeout)
		defer cancel()
		stop := context.AfterFunc(h.runsCtx, cancel)
		defer stop()
		stream, err := h.cfg.Agent.SendMessage(ctx, req)
		if err != nil {
			closeForStreamEnd(c.socket, err)
			return
		}
		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				if ctx.Err() == nil {
					closeForStreamEnd(c.socket, err)
				}
				return
			}
			c.Send(resp)
		}
	}()
}

func (h *Handlers) serveNotifications(w http.ResponseWriter, r *http.Request) {
	c, finish, ok := h.upgrade(w, r, domain.SocketNotifications, "/v1/notifications/live",
		func(_ context.Context, claims auth.Claims) (app.SocketInfo, error) {
			return app.SocketInfo{Kind: domain.SocketNotifications, UserID: claims.Subject.UserID}, nil
		})
	if !ok {
		return
	}
	defer finish()
	code := c.serve(c.claims.ExpiresAt, func() {
		stream, err := h.cfg.Notifications.Subscribe(c.streams, &skeinv1.SubscribeRequest{})
		if err != nil {
			closeForStreamEnd(c.socket, err)
			c.discard()
			return
		}
		go func() {
			for {
				resp, err := stream.Recv()
				if err != nil {
					closeForStreamEnd(c.socket, err)
					return
				}
				c.Send(resp)
			}
		}()
		c.discard()
	})
	h.finished(c, domain.SocketNotifications, code)
}
