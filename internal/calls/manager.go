package calls

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CallState is the lifecycle state of an in-memory call.
type CallState string

const (
	CallStateRinging CallState = "ringing"
	CallStateActive  CallState = "active"
	CallStateEnded   CallState = "ended"
)

// Call is an active WebRTC call kept in memory. Call state is intentionally
// not persisted to PostgreSQL for this MVP.
type Call struct {
	ID              string
	CallerExtension string
	CalleeExtension string
	State           CallState
	CreatedAt       time.Time
	AcceptedAt      *time.Time
}

var (
	ErrCallNotFound     = errors.New("call not found")
	ErrNotParticipant   = errors.New("extension is not a participant of this call")
	ErrInvalidCallState = errors.New("call is not in the required state")
	ErrCallerBusy       = errors.New("caller is already in a call")
	ErrCalleeBusy       = errors.New("callee is already in a call")
	ErrSelfCall         = errors.New("cannot call self")
)

// Manager centralizes in-memory call state. All state transitions are atomic
// under the internal lock, so busy checks and call creation cannot race.
type Manager struct {
	mu    sync.RWMutex
	calls map[string]*Call
}

// NewManager returns an empty call manager.
func NewManager() *Manager {
	return &Manager{calls: make(map[string]*Call)}
}

// StartCall creates a ringing call, rejecting the caller/callee if either is
// already involved in a call.
func (m *Manager) StartCall(callerExt, calleeExt string, now time.Time) (*Call, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if callerExt == calleeExt {
		return nil, ErrSelfCall
	}
	if m.findLocked(callerExt) != nil {
		return nil, ErrCallerBusy
	}
	if m.findLocked(calleeExt) != nil {
		return nil, ErrCalleeBusy
	}
	c := &Call{
		ID:              uuid.NewString(),
		CallerExtension: callerExt,
		CalleeExtension: calleeExt,
		State:           CallStateRinging,
		CreatedAt:       now,
	}
	m.calls[c.ID] = c
	cp := *c
	return &cp, nil
}

// AcceptCall transitions a ringing call to active. Only the callee may accept.
func (m *Manager) AcceptCall(callID, ext string, now time.Time) (*Call, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.calls[callID]
	if !ok {
		return nil, ErrCallNotFound
	}
	if ext != c.CalleeExtension {
		return nil, ErrNotParticipant
	}
	if c.State != CallStateRinging {
		return nil, ErrInvalidCallState
	}
	c.State = CallStateActive
	c.AcceptedAt = &now
	cp := *c
	return &cp, nil
}

// RejectCall ends a ringing call. Only the callee may reject.
func (m *Manager) RejectCall(callID, ext string) (*Call, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.calls[callID]
	if !ok {
		return nil, ErrCallNotFound
	}
	if ext != c.CalleeExtension {
		return nil, ErrNotParticipant
	}
	if c.State != CallStateRinging {
		return nil, ErrInvalidCallState
	}
	c.State = CallStateEnded
	delete(m.calls, callID)
	cp := *c
	return &cp, nil
}

// EndCall ends a call. Either participant may end it.
func (m *Manager) EndCall(callID, ext string) (*Call, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.calls[callID]
	if !ok {
		return nil, ErrCallNotFound
	}
	if ext != c.CallerExtension && ext != c.CalleeExtension {
		return nil, ErrNotParticipant
	}
	c.State = CallStateEnded
	delete(m.calls, callID)
	cp := *c
	return &cp, nil
}

// TimeoutCall ends a call only if it is still ringing. It is a no-op for
// calls that were already accepted or ended, so a late timeout can never kill
// an established call.
func (m *Manager) TimeoutCall(callID string) (*Call, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.calls[callID]
	if !ok {
		return nil, ErrCallNotFound
	}
	if c.State != CallStateRinging {
		return nil, ErrInvalidCallState
	}
	c.State = CallStateEnded
	delete(m.calls, callID)
	cp := *c
	return &cp, nil
}

// GetCall returns a snapshot of a call.
func (m *Manager) GetCall(callID string) (*Call, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, ok := m.calls[callID]
	if !ok {
		return nil, false
	}
	cp := *c
	return &cp, true
}

// FindCallForExtension returns the call an extension is involved in, if any.
func (m *Manager) FindCallForExtension(ext string) (*Call, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c := m.findLocked(ext)
	if c == nil {
		return nil, false
	}
	cp := *c
	return &cp, true
}

// IsBusy reports whether an extension is involved in any call.
func (m *Manager) IsBusy(ext string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.findLocked(ext) != nil
}

func (m *Manager) findLocked(ext string) *Call {
	for _, c := range m.calls {
		if c.CallerExtension == ext || c.CalleeExtension == ext {
			return c
		}
	}
	return nil
}
