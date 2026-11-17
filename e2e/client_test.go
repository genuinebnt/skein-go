//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	// Registers the error detail types, so google.rpc.Status bodies decode.
	_ "google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

// --- environment ------------------------------------------------------------

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func localURL(override, portVar, port string) string {
	return envOr(override, "http://127.0.0.1:"+envOr(portVar, port))
}

var (
	gatewayURL       = localURL("E2E_GATEWAY_URL", "SKEIN_NATIVE_GATEWAY_HTTP", "28080")
	identityAdminURL = localURL("E2E_IDENTITY_ADMIN_URL", "SKEIN_NATIVE_IDENTITY_ADMIN", "28101")
	mailpitURL       = localURL("E2E_MAILPIT_URL", "SKEIN_NATIVE_MAILPIT_PORT", "28025")
	seedDir          = envOr("E2E_SEED_DIR", "../testdata/seed/small")
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

func TestMain(m *testing.M) {
	resp, err := httpClient.Get(gatewayURL + "/readyz")
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "e2e: the gateway at %s is not ready (%v); start the stack with make up-native\n", gatewayURL, err)
		os.Exit(1)
	}
	_ = resp.Body.Close()
	os.Exit(m.Run())
}

// --- identifiers and credentials --------------------------------------------

// randomHex returns n random bytes as hex.
func randomHex(t testing.TB, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// newPassword is generated per run so no credential is ever written down.
func newPassword(t testing.TB) string { return "pw-" + randomHex(t, 12) }

// newReplicaID returns a random replica ID at or above 2^32, the range real
// devices use (docs/seed-data.md).
func newReplicaID(t testing.TB) uint64 {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return binary.BigEndian.Uint64(b[:]) | 1<<63
}

// --- REST -------------------------------------------------------------------

// response is a finished HTTP exchange.
type response struct {
	status int
	header http.Header
	body   []byte
}

// rpcStatus decodes the google.rpc.Status body of an error response.
func (r response) rpcStatus(t testing.TB) *spb.Status {
	t.Helper()
	var st spb.Status
	if err := protojson.Unmarshal(r.body, &st); err != nil {
		t.Fatalf("error body is not a google.rpc.Status (%d): %s", r.status, r.body)
	}
	return &st
}

type requestOption func(*http.Request)

func withHeader(key, value string) requestOption {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// rawRequest sends body (a proto message, a []byte, or nil) with token as
// the bearer when it is not empty.
func rawRequest(t testing.TB, method, url, token string, body any, opts ...requestOption) response {
	t.Helper()
	var data []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		data = b
	case proto.Message:
		var err error
		if data, err = protojson.Marshal(b); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unsupported body %T", body)
	}
	if data == nil && (method == http.MethodPost || method == http.MethodPut) {
		data = []byte("{}")
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: out}
}

// decode fails the test unless r is 200, then decodes it into out.
func decode(t testing.TB, what string, r response, out proto.Message) {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("%s: HTTP %d: %s", what, r.status, r.body)
	}
	if out == nil {
		return
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(r.body, out); err != nil {
		t.Fatalf("%s: decode %T: %v: %s", what, out, err, r.body)
	}
}

// expectError fails the test unless r has HTTP status httpStatus and a
// google.rpc.Status with that gRPC code.
func expectError(t testing.TB, what string, r response, httpStatus int, code int32) *spb.Status {
	t.Helper()
	if r.status != httpStatus {
		t.Fatalf("%s: HTTP %d, want %d: %s", what, r.status, httpStatus, r.body)
	}
	st := r.rpcStatus(t)
	if st.GetCode() != code {
		t.Fatalf("%s: gRPC code %d, want %d: %s", what, st.GetCode(), code, r.body)
	}
	return st
}

// --- users ------------------------------------------------------------------

// user is a signed-in account with its current tokens.
type user struct {
	t        testing.TB
	id       string
	email    string
	name     string
	password string
	access   string
	refresh  string
}

// handle is the user's @handle: the local part of the address, lowercased.
func (u *user) handle() string { return strings.ToLower(strings.SplitN(u.email, "@", 2)[0]) }

// signUp creates a fresh account through the gateway.
func signUp(t testing.TB, label string) *user {
	t.Helper()
	u := &user{t: t, email: fmt.Sprintf("%s%s@e2e.example", label, randomHex(t, 4)), name: strings.ToUpper(label[:1]) + label[1:], password: newPassword(t)}
	var resp skeinv1.SignUpResponse
	decode(t, "sign up "+label, rawRequest(t, http.MethodPost, gatewayURL+"/v1/users", "",
		&skeinv1.SignUpRequest{Email: u.email, Password: u.password, DisplayName: u.name}), &resp)
	u.id, u.access, u.refresh = resp.GetUser().GetId(), resp.GetSession().GetAccessToken(), resp.GetSession().GetRefreshToken()
	if u.id == "" || u.access == "" || u.refresh == "" {
		t.Fatalf("sign up returned %v", &resp)
	}
	return u
}

// seedUser signs in as an existing (seed) user through identity's dev-only
// session route; seed users have no password.
func seedUser(t testing.TB, userID string) *user {
	t.Helper()
	r := rawRequest(t, http.MethodPost, identityAdminURL+"/dev/sessions", "", []byte(`{"userId":"`+userID+`"}`))
	if r.status == http.StatusNotFound && !bytes.Contains(r.body, []byte("no such user")) {
		t.Fatalf("identity has no dev session route; run it with IDENTITY_DEV_SESSIONS=true (make up-native does)")
	}
	var resp skeinv1.SignInResponse
	decode(t, "dev session for "+userID, r, &resp)
	return &user{t: t, id: userID, email: resp.GetUser().GetEmail(), name: resp.GetUser().GetDisplayName(),
		access: resp.GetSession().GetAccessToken(), refresh: resp.GetSession().GetRefreshToken()}
}

// do sends a REST request as u. A 429 is retried after Retry-After, as
// docs/api/README.md tells clients to: every test shares one client IP, so
// parallel tests can exhaust the per-IP budget together. A 429 means the
// request was refused before it ran, so retrying is safe for any method.
func (u *user) do(method, path string, body any, opts ...requestOption) response {
	u.t.Helper()
	deadline := time.Now().Add(longWait)
	for {
		r := u.doOnce(method, path, body, opts...)
		if r.status != http.StatusTooManyRequests || time.Now().After(deadline) {
			return r
		}
		wait, err := strconv.Atoi(r.header.Get("Retry-After"))
		if err != nil || wait < 1 {
			wait = 1
		}
		time.Sleep(time.Duration(wait)*time.Second + time.Duration(randomByte(u.t))*time.Millisecond)
	}
}

// doOnce sends a REST request as u once, whatever the answer.
func (u *user) doOnce(method, path string, body any, opts ...requestOption) response {
	u.t.Helper()
	return rawRequest(u.t, method, gatewayURL+path, u.access, body, opts...)
}

// randomByte is jitter for retries.
func randomByte(t testing.TB) byte {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return b[0]
}

// call sends a REST request as u and decodes a 200 into out.
func (u *user) call(method, path string, body any, out proto.Message, opts ...requestOption) response {
	u.t.Helper()
	r := u.do(method, path, body, opts...)
	decode(u.t, method+" "+path, r, out)
	return r
}

// createWorkspace makes a workspace owned by u.
func (u *user) createWorkspace(name string) string {
	u.t.Helper()
	var resp skeinv1.CreateWorkspaceResponse
	u.call(http.MethodPost, "/v1/workspaces", &skeinv1.CreateWorkspaceRequest{Name: name}, &resp)
	id := resp.GetWorkspaceId()
	if id == "" {
		u.t.Fatalf("create workspace returned %v", &resp)
	}
	return id
}

// addMember gives other a role in workspace (the API adds by user ID).
func (u *user) addMember(workspace string, other *user, role skeinv1.WorkspaceRole) {
	u.t.Helper()
	u.call(http.MethodPut, "/v1/workspaces/"+workspace+"/members/"+other.id,
		&skeinv1.SetWorkspaceRoleRequest{Role: role}, nil)
}

// createNote makes a note with optional Markdown content.
func (u *user) createNote(workspace, title, markdown string) *skeinv1.Note {
	u.t.Helper()
	var resp skeinv1.CreateNoteResponse
	u.call(http.MethodPost, "/v1/workspaces/"+workspace+"/notes",
		&skeinv1.CreateNoteRequest{Title: title, Markdown: markdown}, &resp)
	if resp.GetNote().GetId() == "" {
		u.t.Fatalf("create note returned %v", &resp)
	}
	return resp.GetNote()
}

// markdown reads a note's current Markdown.
func (u *user) markdown(noteID string) string {
	u.t.Helper()
	var resp skeinv1.GetNoteResponse
	u.call(http.MethodGet, "/v1/notes/"+noteID+"?includeMarkdown=true", nil, &resp)
	return resp.GetMarkdown()
}

// --- WebSockets -------------------------------------------------------------

const (
	protocolProto = "skein.v1.proto"
	protocolJSON  = "skein.v1.json"
)

// dial opens a WebSocket at path as u, offering the protobuf encoding.
func (u *user) dial(ctx context.Context, path string) (*websocket.Conn, error) {
	url := "ws" + strings.TrimPrefix(gatewayURL, "http") + path
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{protocolProto, "bearer." + u.access},
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("dial %s: HTTP %d: %w", path, resp.StatusCode, err)
		}
		return nil, fmt.Errorf("dial %s: %w", path, err)
	}
	conn.SetReadLimit(4 << 20)
	return conn, nil
}

func writeProto(ctx context.Context, conn *websocket.Conn, m proto.Message) error {
	data, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageBinary, data)
}

func readProto(ctx context.Context, conn *websocket.Conn, into proto.Message) error {
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	if typ != websocket.MessageBinary {
		return fmt.Errorf("got a %s frame on a %s socket", typ, protocolProto)
	}
	return proto.Unmarshal(data, into)
}

// closeCode is the close code of err, or -1 if the connection ended without
// a close frame.
func closeCode(err error) websocket.StatusCode { return websocket.CloseStatus(err) }

// --- waiting ----------------------------------------------------------------

// longWait bounds anything asynchronous: event consumers, projections,
// caches expiring. Generous, because the whole stack shares two CPUs in CI.
const longWait = 90 * time.Second

// eventually polls check every interval until it reports done or timeout
// passes; then the test fails with the last state check described.
func eventually(t testing.TB, timeout time.Duration, what string, check func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	interval := 200 * time.Millisecond
	var last string
	for {
		done, state := check()
		if done {
			return
		}
		last = state
		if time.Now().After(deadline) {
			t.Fatalf("%s: not true after %s; last state: %s", what, timeout, last)
		}
		time.Sleep(interval)
		interval = min(interval*3/2, 2*time.Second)
	}
}
