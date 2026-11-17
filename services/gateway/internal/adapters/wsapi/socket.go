package wsapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// Subprotocols from docs/api/websocket.md.
const (
	ProtocolJSON   = "skein.v1.json"
	ProtocolProto  = "skein.v1.proto"
	bearerPrefix   = "bearer."
	protocolHeader = "Sec-WebSocket-Protocol"
)

// ErrBadFrame means a frame does not decode as the expected message.
var ErrBadFrame = errors.New("wsapi: frame does not decode")

// encoding is the negotiated frame encoding.
type encoding struct {
	binary bool
}

func (e encoding) messageType() websocket.MessageType {
	if e.binary {
		return websocket.MessageBinary
	}
	return websocket.MessageText
}

func (e encoding) encode(m proto.Message) ([]byte, error) {
	if e.binary {
		return proto.Marshal(m)
	}
	return protojson.Marshal(m)
}

func (e encoding) decode(typ websocket.MessageType, data []byte, into proto.Message) error {
	if typ != e.messageType() {
		return fmt.Errorf("%w: %s frame on a %s connection", ErrBadFrame, typ, e.messageType())
	}
	var err error
	if e.binary {
		err = proto.Unmarshal(data, into)
	} else {
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, into)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadFrame, err)
	}
	return nil
}

// offer is what the client put in Sec-WebSocket-Protocol.
type offer struct {
	enc encoding
	// accept is the subprotocol to echo, "" when the client offered no
	// encoding.
	accept string
	token  string
}

func parseOffer(header []string) offer {
	var o offer
	for _, value := range header {
		for _, entry := range strings.Split(value, ",") {
			entry = strings.TrimSpace(entry)
			switch {
			case strings.HasPrefix(entry, bearerPrefix):
				if o.token == "" {
					o.token = strings.TrimPrefix(entry, bearerPrefix)
				}
			case (entry == ProtocolProto || entry == ProtocolJSON) && o.accept == "":
				o.accept = entry
				o.enc = encoding{binary: entry == ProtocolProto}
			}
		}
	}
	return o
}

// Options tune every socket. Zero values take the documented defaults.
type Options struct {
	PingInterval  time.Duration // 30s
	PongTimeout   time.Duration // 60s
	WriteTimeout  time.Duration // 10s per frame
	FlushTimeout  time.Duration // 2s to flush the queue before closing
	QueueSize     int           // 256 frames
	MaxFrameBytes int64         // 1 MiB
	// StreamDrain bounds how long a sync stream may go on after its socket
	// closed, so documents applies the frames the client sent just before
	// closing; 5s.
	StreamDrain time.Duration
}

func (o Options) withDefaults() Options {
	if o.PingInterval == 0 {
		o.PingInterval = 30 * time.Second
	}
	if o.PongTimeout == 0 {
		o.PongTimeout = 60 * time.Second
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.FlushTimeout == 0 {
		o.FlushTimeout = 2 * time.Second
	}
	if o.StreamDrain == 0 {
		o.StreamDrain = 5 * time.Second
	}
	if o.QueueSize == 0 {
		o.QueueSize = 256
	}
	if o.MaxFrameBytes == 0 {
		o.MaxFrameBytes = 1 << 20
	}
	return o
}

// socket is one live WebSocket (see the package documentation).
type socket struct {
	enc  encoding
	opts Options
	log  *slog.Logger
	conn *websocket.Conn

	// streams is cancelled when the socket starts closing; gRPC streams
	// tied to the socket use it. It is separate from the connection's own
	// lifetime because cancelling a context passed to coder/websocket's
	// Read closes the connection before our close frame could be sent.
	streams context.Context
	stop    context.CancelFunc

	queue   chan []byte
	closing chan struct{}

	mu     sync.Mutex
	closed bool
	code   domain.CloseCode
	reason string
}

func newSocket(ctx context.Context, enc encoding, opts Options, log *slog.Logger) *socket {
	streams, stop := context.WithCancel(ctx)
	return &socket{
		enc: enc, opts: opts, log: log, streams: streams, stop: stop,
		queue: make(chan []byte, opts.QueueSize), closing: make(chan struct{}),
	}
}

// Close implements app.SocketCloser.
func (s *socket) Close(code domain.CloseCode, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed, s.code, s.reason = true, code, domain.CloseReason(reason)
	close(s.closing)
	s.stop()
}

func (s *socket) closeState() (domain.CloseCode, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code, s.reason
}

// Send queues m for the client without blocking. A full queue closes the
// socket with 1013. It reports whether m was queued.
func (s *socket) Send(m proto.Message) bool {
	data, err := s.enc.encode(m)
	if err != nil {
		s.Close(domain.CloseInternalError, "internal error")
		return false
	}
	select {
	case <-s.closing:
		return false
	default:
	}
	select {
	case s.queue <- data:
		return true
	default:
		s.Close(domain.CloseTryAgainLater, "client too slow; reconnect")
		return false
	}
}

// read returns the next frame decoded into into. Errors close the socket
// with the right code before they are returned.
func (s *socket) read(into proto.Message) error {
	typ, data, err := s.conn.Read(context.Background())
	if err != nil {
		switch {
		case errors.Is(err, websocket.ErrMessageTooBig):
			s.Close(domain.CloseMessageTooBig, "frame larger than 1 MiB")
		case websocket.CloseStatus(err) != -1:
			// The client closed; coder/websocket answers its close frame.
			s.Close(domain.CloseNormal, "")
		default:
			s.Close(domain.CloseGoingAway, "connection lost")
		}
		return err
	}
	if err := s.enc.decode(typ, data, into); err != nil {
		s.Close(domain.CloseBadRequest, err.Error())
		return err
	}
	return nil
}

// discard reads and drops frames until the connection ends, so control
// frames (pongs, the client's close) are processed.
func (s *socket) discard() {
	for {
		if _, _, err := s.conn.Read(context.Background()); err != nil {
			if errors.Is(err, websocket.ErrMessageTooBig) {
				s.Close(domain.CloseMessageTooBig, "frame larger than 1 MiB")
			} else {
				s.Close(domain.CloseNormal, "")
			}
			return
		}
	}
}

// serve runs the socket until it closes: writer, pinger and expiry timer
// here, reader as given. It returns the code the socket closed with.
func (s *socket) serve(expires time.Time, reader func()) domain.CloseCode {
	s.conn.SetReadLimit(s.opts.MaxFrameBytes)
	var wg sync.WaitGroup
	wg.Add(3)
	writerDone := make(chan struct{})
	go func() { defer wg.Done(); defer close(writerDone); s.writer() }()
	go func() { defer wg.Done(); s.pinger() }()
	go func() { defer wg.Done(); reader() }()

	if !expires.IsZero() {
		timer := time.AfterFunc(time.Until(expires), func() {
			s.Close(domain.CloseUnauthenticated, "token expired")
		})
		defer timer.Stop()
	}

	<-s.closing
	<-writerDone
	code, reason := s.closeState()
	if err := s.conn.Close(websocket.StatusCode(code), reason); err != nil && !errors.Is(err, net.ErrClosed) {
		s.log.Debug("websocket close handshake failed", slog.String("error", err.Error()))
	}
	wg.Wait()
	return code
}

func (s *socket) writer() {
	for {
		select {
		case data := <-s.queue:
			if !s.write(data) {
				s.Close(domain.CloseGoingAway, "write failed")
				return
			}
		case <-s.closing:
			s.flush()
			return
		}
	}
}

func (s *socket) write(data []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.WriteTimeout)
	defer cancel()
	return s.conn.Write(ctx, s.enc.messageType(), data) == nil
}

// flush sends what is queued, unless the client is the reason for closing
// (it is too slow, or gone).
func (s *socket) flush() {
	code, _ := s.closeState()
	if code == domain.CloseTryAgainLater || code == domain.CloseGoingAway {
		return
	}
	deadline := time.Now().Add(s.opts.FlushTimeout)
	for time.Now().Before(deadline) {
		select {
		case data := <-s.queue:
			if !s.write(data) {
				return
			}
		default:
			return
		}
	}
}

func (s *socket) pinger() {
	ticker := time.NewTicker(s.opts.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.closing:
			return
		case <-ticker.C:
		}
		// Not s.streams: a ping write cut short by cancellation could close
		// the connection before the close frame is sent.
		ctx, cancel := context.WithTimeout(context.Background(), s.opts.PongTimeout)
		err := s.conn.Ping(ctx)
		cancel()
		if err != nil {
			select {
			case <-s.closing:
			default:
				s.Close(domain.CloseGoingAway, "keepalive timeout")
			}
			return
		}
	}
}
