package calls

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func newManager() *Manager { return NewManager() }

func TestStartCall(t *testing.T) {
	m := newManager()
	call, err := m.StartCall("101", "203", time.Now())
	if err != nil {
		t.Fatalf("StartCall: %v", err)
	}
	if call.CallerExtension != "101" || call.CalleeExtension != "203" {
		t.Fatalf("unexpected call: %+v", call)
	}
	if call.State != CallStateRinging {
		t.Fatalf("state = %s, want ringing", call.State)
	}
	if call.ID == "" {
		t.Fatal("call id must be generated server-side")
	}
	if !m.IsBusy("101") || !m.IsBusy("203") || m.IsBusy("305") {
		t.Fatal("busy flags wrong after StartCall")
	}
}

func TestStartCallCallerBusy(t *testing.T) {
	m := newManager()
	now := time.Now()
	if _, err := m.StartCall("101", "203", now); err != nil {
		t.Fatalf("StartCall: %v", err)
	}
	if _, err := m.StartCall("101", "305", now); !errors.Is(err, ErrCallerBusy) {
		t.Fatalf("expected ErrCallerBusy, got %v", err)
	}
}

func TestStartCallCalleeBusy(t *testing.T) {
	m := newManager()
	now := time.Now()
	if _, err := m.StartCall("101", "203", now); err != nil {
		t.Fatalf("StartCall: %v", err)
	}
	if _, err := m.StartCall("305", "203", now); !errors.Is(err, ErrCalleeBusy) {
		t.Fatalf("expected ErrCalleeBusy, got %v", err)
	}
}

func TestStartCallSelf(t *testing.T) {
	m := newManager()
	_, err := m.StartCall("101", "101", time.Now())
	if !errors.Is(err, ErrSelfCall) {
		t.Fatalf("expected ErrSelfCall, got %v", err)
	}
}

func TestAcceptCall(t *testing.T) {
	m := newManager()
	now := time.Now()
	call, _ := m.StartCall("101", "203", now)
	accepted, err := m.AcceptCall(call.ID, "203", now.Add(time.Second))
	if err != nil {
		t.Fatalf("AcceptCall: %v", err)
	}
	if accepted.State != CallStateActive {
		t.Fatalf("state = %s, want active", accepted.State)
	}
	if accepted.AcceptedAt == nil {
		t.Fatal("AcceptedAt must be set")
	}
}

func TestAcceptCallByCallerRejected(t *testing.T) {
	m := newManager()
	call, _ := m.StartCall("101", "203", time.Now())
	if _, err := m.AcceptCall(call.ID, "101", time.Now()); !errors.Is(err, ErrNotParticipant) {
		t.Fatalf("expected ErrNotParticipant, got %v", err)
	}
}

func TestAcceptCallByUnrelated(t *testing.T) {
	m := newManager()
	call, _ := m.StartCall("101", "203", time.Now())
	if _, err := m.AcceptCall(call.ID, "305", time.Now()); !errors.Is(err, ErrNotParticipant) {
		t.Fatalf("expected ErrNotParticipant, got %v", err)
	}
}

func TestDoubleAcceptOnlyOneWins(t *testing.T) {
	m := newManager()
	call, _ := m.StartCall("101", "203", time.Now())

	var mu sync.Mutex
	winners := 0
	losers := 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.AcceptCall(call.ID, "203", time.Now())
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				winners++
			} else if errors.Is(err, ErrInvalidCallState) {
				losers++
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("expected exactly one winner, got %d (losers %d)", winners, losers)
	}
	if got, _ := m.GetCall(call.ID); got.State != CallStateActive {
		t.Fatalf("final state = %s, want active", got.State)
	}
}

func TestAcceptThenRejectInvalidState(t *testing.T) {
	m := newManager()
	call, _ := m.StartCall("101", "203", time.Now())
	if _, err := m.AcceptCall(call.ID, "203", time.Now()); err != nil {
		t.Fatalf("AcceptCall: %v", err)
	}
	if _, err := m.RejectCall(call.ID, "203"); !errors.Is(err, ErrInvalidCallState) {
		t.Fatalf("expected ErrInvalidCallState, got %v", err)
	}
}

func TestRejectCall(t *testing.T) {
	m := newManager()
	call, _ := m.StartCall("101", "203", time.Now())
	rejected, err := m.RejectCall(call.ID, "203")
	if err != nil {
		t.Fatalf("RejectCall: %v", err)
	}
	if rejected.State != CallStateEnded {
		t.Fatalf("state = %s, want ended", rejected.State)
	}
	if _, ok := m.GetCall(call.ID); ok {
		t.Fatal("call should be removed after reject")
	}
	if m.IsBusy("101") || m.IsBusy("203") {
		t.Fatal("extensions should be free after reject")
	}
}

func TestEndCallByEitherParticipant(t *testing.T) {
	m := newManager()
	call, _ := m.StartCall("101", "203", time.Now())
	if _, err := m.EndCall(call.ID, "101"); err != nil {
		t.Fatalf("EndCall by caller: %v", err)
	}
	if _, ok := m.GetCall(call.ID); ok {
		t.Fatal("call should be removed")
	}

	call2, _ := m.StartCall("101", "203", time.Now())
	if _, err := m.EndCall(call2.ID, "203"); err != nil {
		t.Fatalf("EndCall by callee: %v", err)
	}
}

func TestEndCallByUnrelated(t *testing.T) {
	m := newManager()
	call, _ := m.StartCall("101", "203", time.Now())
	if _, err := m.EndCall(call.ID, "305"); !errors.Is(err, ErrNotParticipant) {
		t.Fatalf("expected ErrNotParticipant, got %v", err)
	}
}

func TestTimeoutCallOnlyEndsRinging(t *testing.T) {
	m := newManager()
	now := time.Now()

	// Accepted call must not be ended by a late timeout.
	call, _ := m.StartCall("101", "203", now)
	_, _ = m.AcceptCall(call.ID, "203", now.Add(100*time.Millisecond))
	if _, err := m.TimeoutCall(call.ID); !errors.Is(err, ErrInvalidCallState) {
		t.Fatalf("expected no-op timeout on active call, got %v", err)
	}
	if got, _ := m.GetCall(call.ID); got.State != CallStateActive {
		t.Fatalf("active call state changed to %s", got.State)
	}

	// Ringing call must be ended by timeout.
	call2, err := m.StartCall("305", "999", now)
	if err != nil {
		t.Fatalf("StartCall: %v", err)
	}
	ended, err := m.TimeoutCall(call2.ID)
	if err != nil {
		t.Fatalf("TimeoutCall: %v", err)
	}
	if ended.State != CallStateEnded {
		t.Fatalf("state = %s, want ended", ended.State)
	}
	if _, ok := m.GetCall(call2.ID); ok {
		t.Fatal("timed-out call should be removed")
	}

	// Already-ended call is a no-op.
	if _, err := m.TimeoutCall(call2.ID); !errors.Is(err, ErrCallNotFound) {
		t.Fatalf("expected no-op for ended call, got %v", err)
	}
}

func TestFindCallForExtension(t *testing.T) {
	m := newManager()
	call, _ := m.StartCall("101", "203", time.Now())
	found, ok := m.FindCallForExtension("203")
	if !ok || found.ID != call.ID {
		t.Fatalf("FindCallForExtension(203) = %+v, %v", found, ok)
	}
	_, ok = m.FindCallForExtension("305")
	if ok {
		t.Fatal("305 should not be in any call")
	}
	_, _ = m.EndCall(call.ID, "101")
	if _, ok := m.FindCallForExtension("203"); ok {
		t.Fatal("call should be gone after EndCall")
	}
}

func TestConcurrentStartCallNoDoubleBusy(t *testing.T) {
	m := newManager()
	// Two callers race to call the same callee; exactly one must win.
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.StartCall("305", "203", time.Now())
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("expected exactly one successful start, got %d", success)
	}
}
