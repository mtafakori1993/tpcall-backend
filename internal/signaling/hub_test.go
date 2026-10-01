package signaling

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"tpcall-backend/internal/auth"
	"tpcall-backend/internal/history"
	"tpcall-backend/internal/users"
)

const (
	testJWTSecret = "signaling-test-secret"
	msgTimeout    = 3 * time.Second
)

type testEnv struct {
	ts      *httptest.Server
	store   *users.InMemoryStore
	history *history.InMemoryStore
	svc     *auth.Service
	hub     *Hub
	wsURL   string
	tokens  map[string]string
}

func newTestEnv(t *testing.T, ringTimeout time.Duration) *testEnv {
	t.Helper()
	store := users.NewInMemoryStore()
	callHistory := history.NewInMemoryStore().WithUsers(store)
	svc := auth.NewService(store, []byte(testJWTSecret), time.Hour)
	hub := NewHub(store, callHistory, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte(testJWTSecret), ringTimeout)

	mux := http.NewServeMux()
	mux.Handle("/api/v1/ws", http.HandlerFunc(hub.ServeWS))
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hub.Shutdown(ctx)
		ts.Close()
	})

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws"
	env := &testEnv{ts: ts, store: store, history: callHistory, svc: svc, hub: hub, wsURL: wsURL, tokens: map[string]string{}}

	env.addUser(t, "101", "محمد تفکری")
	env.addUser(t, "203", "حسابداری")
	env.addUser(t, "305", "دبیرخانه")
	return env
}

func (e *testEnv) addUser(t *testing.T, ext, name string) {
	t.Helper()
	u, err := e.store.CreateUser(context.Background(), name, ext, "not-a-real-hash")
	if err != nil {
		t.Fatalf("create user %s: %v", ext, err)
	}
	tok, err := e.svc.TokenFor(u)
	if err != nil {
		t.Fatalf("token for %s: %v", ext, err)
	}
	e.tokens[ext] = tok
}

func (e *testEnv) dial(t *testing.T, ext string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+e.tokens[ext])
	conn, resp, err := websocket.DefaultDialer.Dial(e.wsURL, header)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial %s: %v (status %d)", ext, err, resp.StatusCode)
		}
		t.Fatalf("dial %s: %v", ext, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Synchronize with the server: by the time the pong is delivered the
	// server has registered this session and started its read/write pumps,
	// so a subsequent call can never race an unregistered callee.
	send(t, conn, "ping", "", nil)
	waitForType(t, conn, "pong", msgTimeout)
	return conn
}

type testEnvelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Payload   json.RawMessage `json:"payload"`
}

type wsError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type fromRef struct {
	Extension string `json:"extension"`
	Name      string `json:"name"`
}

type incomingCallPayloadTest struct {
	CallID string  `json:"call_id"`
	From   fromRef `json:"from"`
}

func (e testEnvelope) payloadAs(t *testing.T, v any) {
	t.Helper()
	if len(e.Payload) == 0 {
		t.Fatalf("message %q has no payload", e.Type)
	}
	if err := json.Unmarshal(e.Payload, v); err != nil {
		t.Fatalf("unmarshal payload of %q: %v (%s)", e.Type, err, e.Payload)
	}
}

func (e testEnvelope) callID(t *testing.T) string {
	t.Helper()
	var p struct {
		CallID string `json:"call_id"`
	}
	e.payloadAs(t, &p)
	if p.CallID == "" {
		t.Fatalf("message %q missing call_id (payload %s)", e.Type, e.Payload)
	}
	return p.CallID
}

func send(t *testing.T, conn *websocket.Conn, typ, requestID string, payload any) {
	t.Helper()
	env := map[string]any{"type": typ}
	if requestID != "" {
		env["request_id"] = requestID
	}
	if payload != nil {
		env["payload"] = payload
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write %s: %v", typ, err)
	}
}

// connReader owns the single read loop for a WebSocket connection. Reads never
// use a per-read deadline because gorilla connections become permanently
// unusable after a read timeout; instead the caller applies its own overall
// timeout via next().
type connReader struct {
	conn *websocket.Conn
	ch   chan testEnvelope
	done chan struct{}
	once sync.Once
}

func newConnReader(conn *websocket.Conn) *connReader {
	r := &connReader{conn: conn, ch: make(chan testEnvelope, 16), done: make(chan struct{})}
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				r.stop()
				return
			}
			var env testEnvelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			select {
			case r.ch <- env:
			case <-r.done:
				return
			}
		}
	}()
	return r
}

func (r *connReader) stop() {
	r.once.Do(func() { close(r.done) })
}

func (r *connReader) next(timeout time.Duration) (testEnvelope, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case env := <-r.ch:
		return env, true
	case <-r.done:
		return testEnvelope{}, false
	case <-timer.C:
		return testEnvelope{}, false
	}
}

var readers sync.Map // *websocket.Conn -> *connReader

func readerFor(t *testing.T, conn *websocket.Conn) *connReader {
	t.Helper()
	if r, ok := readers.Load(conn); ok {
		return r.(*connReader)
	}
	r := newConnReader(conn)
	actual, _ := readers.LoadOrStore(conn, r)
	return actual.(*connReader)
}

func waitForType(t *testing.T, conn *websocket.Conn, typ string, timeout time.Duration) testEnvelope {
	t.Helper()
	r := readerFor(t, conn)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		env, ok := r.next(time.Until(deadline))
		if !ok {
			continue
		}
		if env.Type == "error" && typ != "error" {
			var errMsg wsError
			env.payloadAs(t, &errMsg)
			t.Fatalf("received unexpected error while waiting for %q: %+v", typ, errMsg)
		}
		if env.Type == typ {
			return env
		}
	}
	t.Fatalf("timed out after %s waiting for message type %q", timeout, typ)
	return testEnvelope{}
}

// connClosed reports whether the reader observed the connection closing
// (server closed it) rather than the timeout elapsing.
func connClosed(t *testing.T, conn *websocket.Conn, timeout time.Duration) bool {
	t.Helper()
	r := readerFor(t, conn)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-r.done:
		return true
	case <-timer.C:
		return false
	}
}

func expectNoMessage(t *testing.T, conn *websocket.Conn, dur time.Duration) {
	t.Helper()
	r := readerFor(t, conn)
	if env, ok := r.next(dur); ok {
		t.Fatalf("expected no message but received %q (payload %s)", env.Type, env.Payload)
	}
}

func waitForError(t *testing.T, conn *websocket.Conn, code string) wsError {
	t.Helper()
	env := waitForType(t, conn, "error", msgTimeout)
	var e wsError
	env.payloadAs(t, &e)
	if e.Code != code {
		t.Fatalf("error code = %q, want %q", e.Code, code)
	}
	return e
}

func startCall(t *testing.T, caller *websocket.Conn, calleeExt, requestID string) string {
	t.Helper()
	send(t, caller, "call", requestID, map[string]any{"to": calleeExt})

	ring := waitForType(t, caller, "call", msgTimeout)
	if ring.RequestID != requestID {
		t.Fatalf("ring ack request_id = %q, want %q", ring.RequestID, requestID)
	}
	var ack struct {
		CallID string `json:"call_id"`
		Status string `json:"status"`
	}
	ring.payloadAs(t, &ack)
	if ack.Status != "ringing" {
		t.Fatalf("ack status = %q, want ringing", ack.Status)
	}
	return ack.CallID
}

func establishCall(t *testing.T, caller, callee *websocket.Conn, callerExt, calleeExt string) string {
	t.Helper()
	callID := startCall(t, caller, calleeExt, "req-"+callerExt)

	incoming := waitForType(t, callee, "incoming_call", msgTimeout)
	if got := incoming.callID(t); got != callID {
		t.Fatalf("incoming_call call_id = %q, want %q", got, callID)
	}
	var icp incomingCallPayloadTest
	incoming.payloadAs(t, &icp)
	if icp.From.Extension != callerExt {
		t.Fatalf("incoming_call from = %+v, want caller %s", icp.From, callerExt)
	}

	send(t, callee, "accept", "", map[string]any{"call_id": callID})
	accepted := waitForType(t, caller, "accepted", msgTimeout)
	if got := accepted.callID(t); got != callID {
		t.Fatalf("accepted call_id = %q, want %q", got, callID)
	}
	return callID
}

// --- Tests ---

func TestOnlineCallerCanCallOnlineCallee(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := startCall(t, c101, "203", "call-1")
	_ = callID

	incoming := waitForType(t, c203, "incoming_call", msgTimeout)
	var icp incomingCallPayloadTest
	incoming.payloadAs(t, &icp)
	if icp.From.Extension != "101" || icp.From.Name != "محمد تفکری" {
		t.Fatalf("unexpected incoming_call from: %+v", icp.From)
	}
}

func TestCannotCallSelf(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	send(t, c101, "call", "req-self", map[string]any{"to": "101"})
	waitForError(t, c101, "cannot_call_self")
}

func TestUnknownCallee(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	send(t, c101, "call", "req-unknown", map[string]any{"to": "999"})
	waitForError(t, c101, "user_not_found")
}

func TestOfflineCallee(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101") // 203 is registered but not connected
	send(t, c101, "call", "req-off", map[string]any{"to": "203"})
	unavail := waitForType(t, c101, "unavailable", msgTimeout)
	var p struct {
		Extension string `json:"extension"`
		Reason    string `json:"reason"`
	}
	unavail.payloadAs(t, &p)
	if p.Extension != "203" || p.Reason != "offline" {
		t.Fatalf("unexpected unavailable: %+v", p)
	}
}

func TestBusyCallee(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	_ = c203
	startCall(t, c101, "203", "call-a")

	send(t, c305, "call", "req-busy", map[string]any{"to": "203"})
	busy := waitForType(t, c305, "busy", msgTimeout)
	var p struct {
		Extension string `json:"extension"`
	}
	busy.payloadAs(t, &p)
	if p.Extension != "203" {
		t.Fatalf("unexpected busy payload: %+v", p)
	}
}

func TestCallerAlreadyInCall(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	_ = c203
	_ = c305
	startCall(t, c101, "203", "call-a")
	send(t, c101, "call", "req-second", map[string]any{"to": "305"})
	waitForError(t, c101, "already_in_call")
}

func TestAcceptFlow(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	callID := establishCall(t, c101, c203, "101", "203")

	// After acceptance the call is active and offers can be relayed.
	send(t, c101, "offer", "", map[string]any{"call_id": callID, "sdp": "v=0 offer"})
	offer := waitForType(t, c203, "offer", msgTimeout)
	var of struct {
		CallID string `json:"call_id"`
		SDP    string `json:"sdp"`
	}
	offer.payloadAs(t, &of)
	if of.CallID != callID || of.SDP != "v=0 offer" {
		t.Fatalf("unexpected offer: %+v", of)
	}
}

func TestRejectFlow(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := startCall(t, c101, "203", "req-reject")
	send(t, c203, "reject", "", map[string]any{"call_id": callID})
	rejected := waitForType(t, c101, "rejected", msgTimeout)
	if got := rejected.callID(t); got != callID {
		t.Fatalf("rejected call_id = %q, want %q", got, callID)
	}
}

func TestUnrelatedUserCannotAccept(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	callID := startCall(t, c101, "203", "req-x")
	send(t, c305, "accept", "", map[string]any{"call_id": callID})
	waitForError(t, c305, "not_call_participant")

	// The call must still be ringable/acceptable by the real callee.
	send(t, c203, "accept", "", map[string]any{"call_id": callID})
	waitForType(t, c101, "accepted", msgTimeout)
}

func TestUnrelatedUserCannotHangup(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	callID := startCall(t, c101, "203", "req-h")
	send(t, c305, "hangup", "", map[string]any{"call_id": callID})
	waitForError(t, c305, "not_call_participant")

	// Call still usable.
	send(t, c203, "accept", "", map[string]any{"call_id": callID})
	waitForType(t, c101, "accepted", msgTimeout)
}

func TestAcceptByCallerRejected(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	_ = c203
	callID := startCall(t, c101, "203", "req-ac")
	send(t, c101, "accept", "", map[string]any{"call_id": callID})
	waitForError(t, c101, "not_call_participant")
}

func TestOfferForwardedOnlyToCorrectPeer(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	callID := establishCall(t, c101, c203, "101", "203")

	send(t, c101, "offer", "", map[string]any{"call_id": callID, "sdp": "offer-sdp-1"})
	offer := waitForType(t, c203, "offer", msgTimeout)
	var of struct {
		CallID string `json:"call_id"`
		SDP    string `json:"sdp"`
	}
	offer.payloadAs(t, &of)
	if of.SDP != "offer-sdp-1" {
		t.Fatalf("unexpected offer payload: %+v", of)
	}
	expectNoMessage(t, c305, 300*time.Millisecond)
}

func TestOfferOnlyFromCaller(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	callID := establishCall(t, c101, c203, "101", "203")

	send(t, c203, "offer", "", map[string]any{"call_id": callID, "sdp": "wrong-side"})
	waitForError(t, c203, "invalid_sender")
}

func TestAnswerForwardedOnlyToCorrectPeer(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	callID := establishCall(t, c101, c203, "101", "203")

	send(t, c101, "offer", "", map[string]any{"call_id": callID, "sdp": "offer"})
	waitForType(t, c203, "offer", msgTimeout)

	send(t, c203, "answer", "", map[string]any{"call_id": callID, "sdp": "answer-sdp-1"})
	answer := waitForType(t, c101, "answer", msgTimeout)
	var an struct {
		CallID string `json:"call_id"`
		SDP    string `json:"sdp"`
	}
	answer.payloadAs(t, &an)
	if an.SDP != "answer-sdp-1" {
		t.Fatalf("unexpected answer payload: %+v", an)
	}
	expectNoMessage(t, c305, 300*time.Millisecond)
}

func TestICEForwardedOnlyToCorrectPeer(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	callID := establishCall(t, c101, c203, "101", "203")

	candidate := map[string]any{"sdpMid": "0", "sdpMLineIndex": 0, "candidate": "candidate:1234 1 udp 1 192.168.1.10 6000 typ host"}
	send(t, c101, "ice_candidate", "", map[string]any{"call_id": callID, "candidate": candidate})

	ice := waitForType(t, c203, "ice_candidate", msgTimeout)
	var ic struct {
		CallID string          `json:"call_id"`
		Cand   json.RawMessage `json:"candidate"`
	}
	ice.payloadAs(t, &ic)
	if ic.CallID != callID {
		t.Fatalf("unexpected ice call_id: %+v", ic)
	}
	var cand map[string]any
	if err := json.Unmarshal(ic.Cand, &cand); err != nil {
		t.Fatalf("candidate payload: %v", err)
	}
	if cand["candidate"] != "candidate:1234 1 udp 1 192.168.1.10 6000 typ host" {
		t.Fatalf("candidate not forwarded intact: %v", cand)
	}
	expectNoMessage(t, c305, 300*time.Millisecond)

	// Other direction also works.
	send(t, c203, "ice_candidate", "", map[string]any{"call_id": callID, "candidate": map[string]any{"candidate": "candidate:from-203"}})
	ice2 := waitForType(t, c101, "ice_candidate", msgTimeout)
	var ic2 struct {
		Cand json.RawMessage `json:"candidate"`
	}
	ice2.payloadAs(t, &ic2)
	if string(ic2.Cand) != `{"candidate":"candidate:from-203"}` {
		t.Fatalf("candidate not forwarded intact: %s", ic2.Cand)
	}
}

func TestHangupByCaller(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	callID := establishCall(t, c101, c203, "101", "203")

	send(t, c101, "hangup", "", map[string]any{"call_id": callID})
	ended := waitForType(t, c203, "call_ended", msgTimeout)
	var ep struct {
		CallID string `json:"call_id"`
		Reason string `json:"reason"`
	}
	ended.payloadAs(t, &ep)
	if ep.CallID != callID || ep.Reason != "remote_hangup" {
		t.Fatalf("unexpected call_ended: %+v", ep)
	}
}

func TestHangupClearsBusyState(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	callID := startCall(t, c101, "203", "req-h1")
	send(t, c203, "hangup", "", map[string]any{"call_id": callID})
	waitForType(t, c101, "call_ended", msgTimeout)

	// 203 must be available again.
	callID2 := startCall(t, c305, "203", "req-h2")
	_ = callID2
	waitForType(t, c203, "incoming_call", msgTimeout)
}

func TestDisconnectClearsCallState(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := startCall(t, c101, "203", "req-dc")
	_ = callID

	// Callee disconnects while ringing.
	c203.Close()

	ended := waitForType(t, c101, "call_ended", msgTimeout)
	var ep struct {
		Reason string `json:"reason"`
	}
	ended.payloadAs(t, &ep)
	if ep.Reason != "peer_disconnected" {
		t.Fatalf("unexpected reason: %+v", ep)
	}

	// 203 must be callable again.
	c203b := env.dial(t, "203")
	callID2 := startCall(t, c101, "203", "req-dc2")
	_ = callID2
	waitForType(t, c203b, "incoming_call", msgTimeout)
}

func TestRingTimeoutClearsCall(t *testing.T) {
	env := newTestEnv(t, 200*time.Millisecond)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	startCall(t, c101, "203", "req-timeout")

	callerEnded := waitForType(t, c101, "call_ended", msgTimeout)
	var cp struct{ Reason string }
	callerEnded.payloadAs(t, &cp)
	if cp.Reason != "no_answer" {
		t.Fatalf("caller reason = %q, want no_answer", cp.Reason)
	}

	calleeEnded := waitForType(t, c203, "call_ended", msgTimeout)
	var cp2 struct{ Reason string }
	calleeEnded.payloadAs(t, &cp2)
	if cp2.Reason != "ring_timeout" {
		t.Fatalf("callee reason = %q, want ring_timeout", cp2.Reason)
	}

	// Both extensions must be free afterwards.
	callID2 := startCall(t, c305, "203", "req-after-timeout")
	_ = callID2
	waitForType(t, c203, "incoming_call", msgTimeout)
}

func TestLateAcceptAfterTimeoutFails(t *testing.T) {
	env := newTestEnv(t, 150*time.Millisecond)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := startCall(t, c101, "203", "req-late")
	waitForType(t, c101, "call_ended", msgTimeout)

	// The callee accepts too late: the call no longer exists.
	send(t, c203, "accept", "", map[string]any{"call_id": callID})
	waitForError(t, c203, "call_not_found")
}

func TestDuplicateLoginReplacesOldConnection(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c203 := env.dial(t, "203")

	oldConn := env.dial(t, "101")
	newConn := env.dial(t, "101")

	// The old connection receives session_replaced and is closed.
	replaced := waitForType(t, oldConn, "error", msgTimeout)
	var e wsError
	replaced.payloadAs(t, &e)
	if e.Code != "session_replaced" {
		t.Fatalf("error code = %q, want session_replaced", e.Code)
	}
	if !connClosed(t, oldConn, msgTimeout) {
		t.Fatal("old connection should be closed after replacement")
	}

	// The new connection is fully functional.
	callID := startCall(t, newConn, "203", "req-replace")
	_ = callID
	waitForType(t, c203, "incoming_call", msgTimeout)
}

func TestOfflineDetectedAfterReconnectGap(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")

	// 203 connects then immediately disconnects.
	c203 := env.dial(t, "203")
	c203.Close()
	time.Sleep(50 * time.Millisecond)

	send(t, c101, "call", "req-gap", map[string]any{"to": "203"})
	unavail := waitForType(t, c101, "unavailable", msgTimeout)
	var p struct{ Reason string }
	unavail.payloadAs(t, &p)
	if p.Reason != "offline" {
		t.Fatalf("reason = %q, want offline", p.Reason)
	}
}

func TestPingPong(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	send(t, c101, "ping", "", nil)
	waitForType(t, c101, "pong", msgTimeout)
}

func TestMalformedMessageDoesNotKillConnection(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")

	if err := c101.WriteMessage(websocket.TextMessage, []byte("{not-json")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForError(t, c101, "invalid_message")

	// Connection still works.
	send(t, c101, "call", "req-after-garbage", map[string]any{"to": "999"})
	waitForError(t, c101, "user_not_found")
}

func TestUnknownMessageType(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	send(t, c101, "teleport", "", nil)
	waitForError(t, c101, "invalid_message")
}

func TestWebSocketRequiresAuth(t *testing.T) {
	env := newTestEnv(t, time.Second)
	// No token at all.
	_, resp, err := websocket.DefaultDialer.Dial(env.wsURL, nil)
	if err == nil {
		t.Fatal("expected dial to fail without token")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	// Garbage token.
	header := http.Header{}
	header.Set("Authorization", "Bearer garbage")
	_, resp, err = websocket.DefaultDialer.Dial(env.wsURL, header)
	if err == nil {
		t.Fatal("expected dial to fail with bad token")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestWebSocketAuthViaQueryToken(t *testing.T) {
	env := newTestEnv(t, time.Second)
	conn, _, err := websocket.DefaultDialer.Dial(env.wsURL+"?token="+env.tokens["101"], nil)
	if err != nil {
		t.Fatalf("dial with query token: %v", err)
	}
	defer conn.Close()

	send(t, conn, "call", "req-query", map[string]any{"to": "999"})
	waitForError(t, conn, "user_not_found")
}

func TestConcurrentAcceptOnlyOneWinsOverWire(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := startCall(t, c101, "203", "req-race")

	// Two accepts back to back; the second must lose.
	send(t, c203, "accept", "", map[string]any{"call_id": callID})
	waitForType(t, c101, "accepted", msgTimeout)
	send(t, c203, "accept", "", map[string]any{"call_id": callID})
	waitForError(t, c203, "invalid_call_state")
}

func TestPresenceReplacementRace(t *testing.T) {
	env := newTestEnv(t, time.Second)

	cA := env.dial(t, "101")
	cB := env.dial(t, "101")

	// A is replaced by B; A must not unregister B when it closes.
	time.Sleep(100 * time.Millisecond)
	if !env.hub.IsOnline("101") {
		t.Fatal("extension 101 should remain online after replacement")
	}
	if _, ok := env.hub.presence.Get("101"); !ok {
		t.Fatal("presence lost after replacement")
	}
	_ = cA
	_ = cB
}

func TestBusyStateReleasedAfterPeerHangup(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	c305 := env.dial(t, "305")

	callID := establishCall(t, c101, c203, "101", "203")
	send(t, c203, "hangup", "", map[string]any{"call_id": callID})
	waitForType(t, c101, "call_ended", msgTimeout)

	// Caller 101 must be free again.
	callID2 := startCall(t, c101, "305", "req-free")
	_ = callID2
	waitForType(t, c305, "incoming_call", msgTimeout)
}

func TestOfferBeforeAcceptIsRejected(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")
	_ = c203

	callID := startCall(t, c101, "203", "req-early")
	send(t, c101, "offer", "", map[string]any{"call_id": callID, "sdp": "early"})
	waitForError(t, c101, "invalid_call_state")
}
