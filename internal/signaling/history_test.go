package signaling

import (
	"context"
	"testing"
	"time"

	"tpcall-backend/internal/history"
)

// findRecord returns the single stored record with the given call id, failing
// the test if zero or more than one match (the "one physical call = one row"
// invariant).
func (e *testEnv) findRecord(t *testing.T, callID string) history.Record {
	t.Helper()
	var found []history.Record
	for _, r := range e.history.Records(context.Background()) {
		if r.CallID.String() == callID {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one history row for call %s, got %d", callID, len(found))
	}
	return found[0]
}

func TestAnsweredOutgoingCallStoredOnce(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := establishCall(t, c101, c203, "101", "203")

	// Callee hangs up after a completed conversation.
	send(t, c203, "hangup", "", map[string]any{"call_id": callID})
	waitForType(t, c101, "call_ended", msgTimeout)

	rec := env.findRecord(t, callID)
	if rec.CallerUserID.String() != env.userID("101") {
		t.Fatalf("caller_user_id = %s, want 101", rec.CallerUserID)
	}
	if rec.CalleeUserID.String() != env.userID("203") {
		t.Fatalf("callee_user_id = %s, want 203", rec.CalleeUserID)
	}
	if rec.AnsweredAt == nil {
		t.Fatal("answered_at must be set for an accepted call")
	}
	if rec.EndedAt == nil {
		t.Fatal("ended_at must be set after hangup")
	}
	if rec.EndReason != history.EndReasonCompleted {
		t.Fatalf("end_reason = %q, want %q", rec.EndReason, history.EndReasonCompleted)
	}
	if rec.EndedAt.Sub(*rec.AnsweredAt) <= 0 {
		t.Fatal("duration must be positive")
	}
	if len(env.history.Records(context.Background())) != 1 {
		t.Fatalf("expected a single history row, got %d", len(env.history.Records(context.Background())))
	}
}

func TestMissedCallAfterRingTimeout(t *testing.T) {
	env := newTestEnv(t, 150*time.Millisecond)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := startCall(t, c101, "203", "req-timeout")
	waitForType(t, c101, "call_ended", msgTimeout)
	waitForType(t, c203, "call_ended", msgTimeout)

	rec := env.findRecord(t, callID)
	if rec.AnsweredAt != nil {
		t.Fatal("answered_at must stay null for a missed call")
	}
	if rec.EndedAt == nil {
		t.Fatal("ended_at must be set after ring timeout")
	}
	if rec.EndReason != history.EndReasonNoAnswer {
		t.Fatalf("end_reason = %q, want %q", rec.EndReason, history.EndReasonNoAnswer)
	}
}

func TestRejectedCallStored(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := startCall(t, c101, "203", "req-reject")
	send(t, c203, "reject", "", map[string]any{"call_id": callID})
	waitForType(t, c101, "rejected", msgTimeout)

	rec := env.findRecord(t, callID)
	if rec.AnsweredAt != nil {
		t.Fatal("answered_at must stay null for a rejected call")
	}
	if rec.EndReason != history.EndReasonRejected {
		t.Fatalf("end_reason = %q, want %q", rec.EndReason, history.EndReasonRejected)
	}
}

func TestCallerCancellationWhileRinging(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := startCall(t, c101, "203", "req-cancel")
	send(t, c101, "hangup", "", map[string]any{"call_id": callID})
	waitForType(t, c203, "call_ended", msgTimeout)

	rec := env.findRecord(t, callID)
	if rec.AnsweredAt != nil {
		t.Fatal("answered_at must stay null when the caller cancels while ringing")
	}
	if rec.EndReason != history.EndReasonCallerCancelled {
		t.Fatalf("end_reason = %q, want %q", rec.EndReason, history.EndReasonCallerCancelled)
	}
}

func TestCalleeCancellationWhileRinging(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := startCall(t, c101, "203", "req-cancel2")
	// Callee hangs up instead of sending an explicit reject.
	send(t, c203, "hangup", "", map[string]any{"call_id": callID})
	waitForType(t, c101, "call_ended", msgTimeout)

	rec := env.findRecord(t, callID)
	if rec.AnsweredAt != nil {
		t.Fatal("answered_at must stay null when the callee cancels while ringing")
	}
	if rec.EndReason != history.EndReasonCalleeCancelled {
		t.Fatalf("end_reason = %q, want %q", rec.EndReason, history.EndReasonCalleeCancelled)
	}
}

func TestPeerDisconnectWhileActiveStored(t *testing.T) {
	env := newTestEnv(t, time.Second)
	c101 := env.dial(t, "101")
	c203 := env.dial(t, "203")

	callID := establishCall(t, c101, c203, "101", "203")
	c203.Close() // peer drops while the call is active
	waitForType(t, c101, "call_ended", msgTimeout)

	rec := env.findRecord(t, callID)
	if rec.AnsweredAt == nil {
		t.Fatal("answered_at must be set; the call was answered before the disconnect")
	}
	if rec.EndReason != history.EndReasonPeerDisconnected {
		t.Fatalf("end_reason = %q, want %q", rec.EndReason, history.EndReasonPeerDisconnected)
	}
}

// userID returns the user uuid for an extension.
func (e *testEnv) userID(ext string) string {
	u, err := e.store.GetUserByExtension(context.Background(), ext)
	if err != nil {
		panic(err)
	}
	return u.ID.String()
}
