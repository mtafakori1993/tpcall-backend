package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"tpcall-backend/internal/auth"
	"tpcall-backend/internal/calls"
	"tpcall-backend/internal/history"
	"tpcall-backend/internal/httpx"
	"tpcall-backend/internal/users"
)

// historyWriteTimeout bounds each call-history write so a slow database can
// never stall the WebSocket pumps (real-time signaling stays authoritative).
const historyWriteTimeout = 2 * time.Second

// Hub owns presence, in-memory calls, and the WebSocket protocol.
// It is safe for concurrent use.
type Hub struct {
	users       users.Store
	history     history.Store
	presence    *Presence
	calls       *calls.Manager
	logger      *slog.Logger
	jwtSecret   []byte
	ringTimeout time.Duration
	upgrader    websocket.Upgrader
}

// NewHub returns a ready-to-use hub.
func NewHub(store users.Store, callHistory history.Store, logger *slog.Logger, jwtSecret []byte, ringTimeout time.Duration) *Hub {
	return &Hub{
		users:       store,
		history:     callHistory,
		presence:    NewPresence(),
		calls:       calls.NewManager(),
		logger:      logger,
		jwtSecret:   jwtSecret,
		ringTimeout: ringTimeout,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
	}
}

// IsOnline reports whether an extension currently has a WebSocket session.
func (h *Hub) IsOnline(extension string) bool {
	return h.presence.IsOnline(extension)
}

// ServeWS authenticates the request (Authorization header or ?token= query
// parameter), upgrades to a WebSocket, and runs the read/write pumps until the
// connection ends.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	token := ""
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	claims, err := auth.ParseToken(h.jwtSecret, token)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing token")
		return
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing token")
		return
	}
	dbCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	user, err := h.users.GetUserByID(dbCtx, id)
	cancel()
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing token")
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an HTTP error response.
		return
	}
	c := newClient(h, conn, user.ID, user.Extension, user.Name)

	if old := h.presence.Register(c); old != nil {
		h.logger.Info("session_replaced",
			"extension", c.extension,
			"old_conn_id", old.connID,
			"new_conn_id", c.connID,
		)
		h.sendError(old, "", "session_replaced", "session replaced by a new connection")
		old.close()
		// End any call the replaced session was participating in so the
		// extension never stays busy after a reconnect.
		if call, ok := h.calls.FindCallForExtension(c.extension); ok {
			h.endCallByDisconnect(call, c.extension)
		}
	}

	h.logger.Info("websocket_connected",
		"extension", c.extension,
		"user_id", c.userID.String(),
		"conn_id", c.connID,
		"remote_addr", r.RemoteAddr,
	)

	go c.writePump()
	c.readPump()
}

// Shutdown closes every live connection so pumps exit and state is cleaned up.
func (h *Hub) Shutdown(ctx context.Context) {
	for _, c := range h.presence.Clients() {
		h.logger.Info("websocket_closing", "extension", c.extension, "conn_id", c.connID)
		c.close()
	}
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
	}
}

// handleMessage dispatches a single inbound message for a client.
func (h *Hub) handleMessage(c *Client, data []byte) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		h.sendError(c, "", "invalid_message", "message must be valid JSON")
		return
	}
	if env.Type == "" {
		h.sendError(c, env.RequestID, "invalid_message", "message type is required")
		return
	}
	if len(env.Payload) > 0 && !json.Valid(env.Payload) {
		h.sendError(c, env.RequestID, "invalid_message", "payload must be valid JSON")
		return
	}

	switch env.Type {
	case "call":
		h.handleCall(c, env)
	case "accept":
		h.handleAccept(c, env)
	case "reject":
		h.handleReject(c, env)
	case "offer":
		h.handleOffer(c, env)
	case "answer":
		h.handleAnswer(c, env)
	case "ice_candidate":
		h.handleICE(c, env)
	case "hangup":
		h.handleHangup(c, env)
	case "ping":
		h.sendToClient(c, Envelope{Type: "pong"})
	case "pong":
		// Ignore application-level pong; protocol-level pong is handled by gorilla.
	default:
		h.sendError(c, env.RequestID, "invalid_message", "unknown message type")
	}
}

func (h *Hub) handleCall(c *Client, env Envelope) {
	var req callRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		h.sendError(c, env.RequestID, "invalid_payload", "invalid call payload")
		return
	}
	to := strings.TrimSpace(req.To)

	if to == c.extension {
		h.sendError(c, env.RequestID, "cannot_call_self", "cannot call yourself")
		return
	}
	if !users.ValidExtension(to) {
		h.sendError(c, env.RequestID, "user_not_found", "destination extension does not exist")
		return
	}

	dbCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	callee, err := h.users.GetUserByExtension(dbCtx, to)
	cancel()
	if err != nil {
		h.sendError(c, env.RequestID, "user_not_found", "destination extension does not exist")
		return
	}

	if !h.presence.IsOnline(to) {
		h.sendToClient(c, envelope("unavailable", env.RequestID, unavailablePayload{
			Extension: to, Reason: "offline",
		}))
		h.logger.Info("call_unavailable", "caller_extension", c.extension, "callee_extension", to, "reason", "offline")
		return
	}

	call, err := h.calls.StartCall(c.extension, to, time.Now())
	if err != nil {
		switch {
		case errors.Is(err, calls.ErrCallerBusy):
			h.sendError(c, env.RequestID, "already_in_call", "you are already in a call")
		case errors.Is(err, calls.ErrCalleeBusy):
			h.sendToClient(c, envelope("busy", env.RequestID, busyPayload{Extension: to}))
			h.logger.Info("call_busy", "caller_extension", c.extension, "callee_extension", to)
		case errors.Is(err, calls.ErrSelfCall):
			h.sendError(c, env.RequestID, "cannot_call_self", "cannot call yourself")
		default:
			h.logger.Error("start_call_failed", "error", err)
		}
		return
	}

	// The callee may have disconnected between the online check and now.
	if !h.presence.IsOnline(to) {
		_, _ = h.calls.EndCall(call.ID, c.extension)
		h.sendToClient(c, envelope("unavailable", env.RequestID, unavailablePayload{
			Extension: to, Reason: "offline",
		}))
		return
	}

	// The call is now valid and ringing: persist one authoritative history row.
	h.persistCallCreated(call, c.userID, callee.ID, time.Now())

	h.sendToExtension(to, envelope("incoming_call", "", incomingCallPayload{
		CallID: call.ID,
		From:   userRef{Extension: c.extension, Name: c.name},
	}))
	h.sendToClient(c, envelope("call", env.RequestID, ringAckPayload{
		CallID: call.ID,
		Status: "ringing",
		To:     userRef{Extension: callee.Extension, Name: callee.Name},
	}))
	h.logger.Info("call_started",
		"call_id", call.ID,
		"caller_extension", c.extension,
		"callee_extension", to,
	)
	h.scheduleRingTimeout(call.ID)
}

func (h *Hub) handleAccept(c *Client, env Envelope) {
	var req callIDRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		h.sendError(c, env.RequestID, "invalid_payload", "invalid accept payload")
		return
	}
	call, err := h.calls.AcceptCall(req.CallID, c.extension, time.Now())
	if err != nil {
		h.sendCallControlError(c, env.RequestID, err)
		return
	}
	h.persistAnswered(call, time.Now())
	h.sendToExtension(call.CallerExtension, envelope("accepted", "", callIDPayload{CallID: call.ID}))
	h.logger.Info("call_accepted", "call_id", call.ID, "caller_extension", call.CallerExtension, "callee_extension", call.CalleeExtension)
}

func (h *Hub) handleReject(c *Client, env Envelope) {
	var req callIDRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		h.sendError(c, env.RequestID, "invalid_payload", "invalid reject payload")
		return
	}
	call, err := h.calls.RejectCall(req.CallID, c.extension)
	if err != nil {
		h.sendCallControlError(c, env.RequestID, err)
		return
	}
	h.persistEnded(call, history.EndReasonRejected, time.Now())
	h.sendToExtension(call.CallerExtension, envelope("rejected", "", callIDPayload{CallID: call.ID}))
	h.logCallEnded(call, "rejected")
}

func (h *Hub) handleHangup(c *Client, env Envelope) {
	var req callIDRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		h.sendError(c, env.RequestID, "invalid_payload", "invalid hangup payload")
		return
	}
	call, err := h.calls.EndCall(req.CallID, c.extension)
	if err != nil {
		h.sendCallControlError(c, env.RequestID, err)
		return
	}
	h.persistEnded(call, h.endReasonForHangup(call, c.extension), time.Now())
	peer := h.otherParticipant(call, c.extension)
	h.sendCallEnded(peer, call.ID, "remote_hangup")
	h.logCallEnded(call, "remote_hangup")
}

func (h *Hub) handleOffer(c *Client, env Envelope) {
	var req sdpRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		h.sendError(c, env.RequestID, "invalid_payload", "invalid offer payload")
		return
	}
	call, ok := h.requireActiveCall(c, env.RequestID, req.CallID)
	if !ok {
		return
	}
	if c.extension != call.CallerExtension {
		h.sendError(c, env.RequestID, "invalid_sender", "only the caller may send the offer")
		return
	}
	h.sendToExtension(call.CalleeExtension, Envelope{Type: "offer", Payload: env.Payload})
}

func (h *Hub) handleAnswer(c *Client, env Envelope) {
	var req sdpRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		h.sendError(c, env.RequestID, "invalid_payload", "invalid answer payload")
		return
	}
	call, ok := h.requireActiveCall(c, env.RequestID, req.CallID)
	if !ok {
		return
	}
	if c.extension != call.CalleeExtension {
		h.sendError(c, env.RequestID, "invalid_sender", "only the callee may send the answer")
		return
	}
	h.sendToExtension(call.CallerExtension, Envelope{Type: "answer", Payload: env.Payload})
}

func (h *Hub) handleICE(c *Client, env Envelope) {
	var req iceRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		h.sendError(c, env.RequestID, "invalid_payload", "invalid ice_candidate payload")
		return
	}
	call, ok := h.requireActiveCall(c, env.RequestID, req.CallID)
	if !ok {
		return
	}
	peer := h.otherParticipant(call, c.extension)
	h.sendToExtension(peer, Envelope{Type: "ice_candidate", Payload: env.Payload})
}

// requireActiveCall checks the call exists, is active, and the sender is a
// participant. It returns a snapshot of the call.
func (h *Hub) requireActiveCall(c *Client, requestID, callID string) (*calls.Call, bool) {
	call, ok := h.calls.GetCall(callID)
	if !ok {
		h.sendError(c, requestID, "call_not_found", "call does not exist")
		return nil, false
	}
	if c.extension != call.CallerExtension && c.extension != call.CalleeExtension {
		h.sendError(c, requestID, "not_call_participant", "you are not a participant of this call")
		return nil, false
	}
	if call.State != calls.CallStateActive {
		h.sendError(c, requestID, "invalid_call_state", "call is not active")
		return nil, false
	}
	return call, true
}

func (h *Hub) sendCallControlError(c *Client, requestID string, err error) {
	switch {
	case errors.Is(err, calls.ErrCallNotFound):
		h.sendError(c, requestID, "call_not_found", "call does not exist")
	case errors.Is(err, calls.ErrNotParticipant):
		h.sendError(c, requestID, "not_call_participant", "you are not the callee of this call")
	case errors.Is(err, calls.ErrInvalidCallState):
		h.sendError(c, requestID, "invalid_call_state", "call is not in the required state")
	default:
		h.logger.Error("call_control_failed", "error", err)
		h.sendError(c, requestID, "internal_error", "internal signaling error")
	}
}

// handleDisconnect cleans up a client after its socket closed.
func (h *Hub) handleDisconnect(c *Client) {
	removed := h.presence.Unregister(c)
	h.logger.Info("websocket_disconnected",
		"extension", c.extension,
		"user_id", c.userID.String(),
		"conn_id", c.connID,
		"removed", removed,
	)
	if removed {
		if call, ok := h.calls.FindCallForExtension(c.extension); ok {
			h.endCallByDisconnect(call, c.extension)
		}
	}
}

// endCallByDisconnect ends the call the disconnected extension was part of and
// notifies the remaining peer.
func (h *Hub) endCallByDisconnect(call *calls.Call, disconnectedExt string) {
	ended, err := h.calls.EndCall(call.ID, disconnectedExt)
	if err != nil {
		return
	}
	h.persistEnded(ended, history.EndReasonPeerDisconnected, time.Now())
	peer := h.otherParticipant(ended, disconnectedExt)
	h.sendCallEnded(peer, ended.ID, "peer_disconnected")
	h.logCallEnded(ended, "peer_disconnected")
}

// scheduleRingTimeout ends a ringing call after the configured timeout unless
// it has already been accepted or ended.
func (h *Hub) scheduleRingTimeout(callID string) {
	if h.ringTimeout <= 0 {
		return
	}
	go func() {
		timer := time.NewTimer(h.ringTimeout)
		defer timer.Stop()
		<-timer.C

		call, err := h.calls.TimeoutCall(callID)
		if err != nil {
			// No-op: the call was accepted, hung up, or already ended.
			return
		}
		h.persistEnded(call, history.EndReasonNoAnswer, time.Now())
		h.sendCallEnded(call.CallerExtension, call.ID, "no_answer")
		h.sendCallEnded(call.CalleeExtension, call.ID, "ring_timeout")
		h.logCallEnded(call, "ring_timeout")
	}()
}

func (h *Hub) otherParticipant(call *calls.Call, ext string) string {
	if call.CallerExtension == ext {
		return call.CalleeExtension
	}
	return call.CallerExtension
}

// sendToClient enqueues an outbound message. If the send queue is full the
// client is considered unhealthy and disconnected (bounded backpressure).
func (h *Hub) sendToClient(c *Client, env Envelope) bool {
	if c == nil {
		return false
	}
	data, err := json.Marshal(env)
	if err != nil {
		return false
	}
	select {
	case c.send <- data:
		return true
	default:
		h.logger.Warn("send_queue_full",
			"extension", c.extension,
			"conn_id", c.connID,
			"message_type", env.Type,
		)
		c.close()
		return false
	}
}

// sendToExtension delivers an envelope to the current session of an extension,
// if any. Signaling is never broadcast globally.
func (h *Hub) sendToExtension(extension string, env Envelope) {
	c, ok := h.presence.Get(extension)
	if !ok {
		return
	}
	h.sendToClient(c, env)
}

func (h *Hub) sendCallEnded(extension, callID, reason string) {
	h.sendToExtension(extension, envelope("call_ended", "", callEndedPayload{
		CallID: callID, Reason: reason,
	}))
}

func (h *Hub) sendError(c *Client, requestID, code, message string) {
	h.sendToClient(c, envelope("error", requestID, errorPayload{Code: code, Message: message}))
}

func (h *Hub) logCallEnded(call *calls.Call, reason string) {
	h.logger.Info("call_ended",
		"call_id", call.ID,
		"caller_extension", call.CallerExtension,
		"callee_extension", call.CalleeExtension,
		"reason", reason,
		"duration_ms", time.Since(call.CreatedAt).Milliseconds(),
	)
}

// persistCallCreated writes the authoritative "call started" history row. A
// database error is logged and never allowed to break the live call.
func (h *Hub) persistCallCreated(call *calls.Call, callerID, calleeID uuid.UUID, startedAt time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), historyWriteTimeout)
	defer cancel()
	callID, err := uuid.Parse(call.ID)
	if err != nil {
		h.logger.Warn("call_history_invalid_call_id", "call_id", call.ID)
		return
	}
	if err := h.history.CreateCall(ctx, callID, callerID, calleeID, startedAt); err != nil {
		h.logger.Error("call_history_created_failed",
			"call_id", call.ID,
			"caller_extension", call.CallerExtension,
			"callee_extension", call.CalleeExtension,
			"error", err.Error(),
		)
		return
	}
	h.logger.Info("call_history_created",
		"call_id", call.ID,
		"caller_extension", call.CallerExtension,
		"callee_extension", call.CalleeExtension,
	)
}

func (h *Hub) persistAnswered(call *calls.Call, answeredAt time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), historyWriteTimeout)
	defer cancel()
	callID, err := uuid.Parse(call.ID)
	if err != nil {
		h.logger.Warn("call_history_invalid_call_id", "call_id", call.ID)
		return
	}
	if err := h.history.MarkAnswered(ctx, callID, answeredAt); err != nil {
		h.logger.Error("call_history_answered_failed",
			"call_id", call.ID,
			"caller_extension", call.CallerExtension,
			"callee_extension", call.CalleeExtension,
			"error", err.Error(),
		)
		return
	}
	h.logger.Info("call_history_answered",
		"call_id", call.ID,
		"caller_extension", call.CallerExtension,
		"callee_extension", call.CalleeExtension,
	)
}

// persistEnded writes the termination event. It is idempotent at the store
// level, so racing end paths (hangup + disconnect, reject + timeout) still
// leave exactly one row with the first recorded reason.
func (h *Hub) persistEnded(call *calls.Call, endReason string, endedAt time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), historyWriteTimeout)
	defer cancel()
	callID, err := uuid.Parse(call.ID)
	if err != nil {
		h.logger.Warn("call_history_invalid_call_id", "call_id", call.ID)
		return
	}
	if err := h.history.MarkEnded(ctx, callID, endedAt, endReason); err != nil {
		h.logger.Error("call_history_completed_failed",
			"call_id", call.ID,
			"caller_extension", call.CallerExtension,
			"callee_extension", call.CalleeExtension,
			"end_reason", endReason,
			"error", err.Error(),
		)
		return
	}
	duration := ""
	if call.AcceptedAt != nil {
		duration = endedAt.Sub(*call.AcceptedAt).String()
	}
	h.logger.Info("call_history_completed",
		"call_id", call.ID,
		"caller_extension", call.CallerExtension,
		"callee_extension", call.CalleeExtension,
		"end_reason", endReason,
		"duration", duration,
	)
}

// endReasonForHangup normalizes a hangup into the canonical database reason:
// an answered call that was hung up is "completed"; hanging up while still
// ringing is a cancellation by whichever participant hung up.
func (h *Hub) endReasonForHangup(call *calls.Call, hangerExt string) string {
	if call.AcceptedAt != nil {
		return history.EndReasonCompleted
	}
	if call.CallerExtension == hangerExt {
		return history.EndReasonCallerCancelled
	}
	return history.EndReasonCalleeCancelled
}

// OnlineCount returns the number of extensions with a live WebSocket session.
func (h *Hub) OnlineCount() int {
	return len(h.presence.Clients())
}

// Disconnect closes the live session of an extension (used when an admin removes
// a user or resets their password). It reports whether a session existed.
func (h *Hub) Disconnect(extension string) bool {
	c, ok := h.presence.Get(extension)
	if !ok {
		return false
	}
	h.sendError(c, "", "session_closed", "session closed by an administrator")
	c.close()
	return true
}
