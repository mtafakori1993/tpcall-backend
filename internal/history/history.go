package history

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"tpcall-backend/internal/users"
)

// Normalized end reasons persisted to PostgreSQL. These are the canonical
// database values; signaling reasons are mapped to them by the hub.
const (
	EndReasonCompleted        = "completed"
	EndReasonRejected         = "rejected"
	EndReasonNoAnswer         = "no_answer"
	EndReasonCallerCancelled  = "caller_cancelled"
	EndReasonCalleeCancelled  = "callee_cancelled"
	EndReasonPeerDisconnected = "peer_disconnected"
)

// ErrNotFound is returned when a call id has no history record.
var ErrNotFound = errors.New("call history not found")

// Record is one persisted physical call.
type Record struct {
	ID           uuid.UUID
	CallID       uuid.UUID
	CallerUserID uuid.UUID
	CalleeUserID uuid.UUID
	StartedAt    time.Time
	AnsweredAt   *time.Time
	EndedAt      *time.Time
	EndReason    string

	// Peer presentation fields resolved from the users table. They are only
	// populated by ListForUser so the API can build normalized responses
	// without extra lookups.
	CallerName      string
	CallerExtension string
	CalleeName      string
	CalleeExtension string
}

// Store is the persistence abstraction for call history.
type Store interface {
	// CreateCall inserts a new ringing call. Duplicate call ids are ignored so
	// a racing caller can never produce two rows for the same call.
	CreateCall(ctx context.Context, callID uuid.UUID, callerID, calleeID uuid.UUID, startedAt time.Time) error
	// MarkAnswered records when the callee accepted. It never overwrites an
	// existing answered timestamp.
	MarkAnswered(ctx context.Context, callID uuid.UUID, answeredAt time.Time) error
	// MarkEnded records when the call terminated. It is idempotent: the first
	// end event wins and repeated end processing cannot create duplicates.
	MarkEnded(ctx context.Context, callID uuid.UUID, endedAt time.Time, endReason string) error
	// ListForUser returns the history of calls the user was caller or callee
	// of, newest first. hasMore reports whether another page exists.
	ListForUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]Record, bool, error)
}

// InMemoryStore is a thread-safe Store used by tests.
type InMemoryStore struct {
	mu    sync.RWMutex
	rows  []Record // newest call is appended last; ListForUser sorts by started_at DESC
	users users.Store
}

// NewInMemoryStore returns an empty in-memory history store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{}
}

// WithUsers attaches a user store so ListForUser resolves peer names and
// extensions exactly like the Postgres store's JOIN does. Returns the store
// for chaining.
func (s *InMemoryStore) WithUsers(store users.Store) *InMemoryStore {
	s.users = store
	return s
}

func (s *InMemoryStore) CreateCall(ctx context.Context, callID uuid.UUID, callerID, calleeID uuid.UUID, startedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.CallID == callID {
			return nil // already recorded; keep one row per call
		}
	}
	s.rows = append(s.rows, Record{
		ID:           uuid.New(),
		CallID:       callID,
		CallerUserID: callerID,
		CalleeUserID: calleeID,
		StartedAt:    startedAt,
		EndReason:    EndReasonNoAnswer, // default until a real end reason arrives
	})
	return nil
}

func (s *InMemoryStore) MarkAnswered(ctx context.Context, callID uuid.UUID, answeredAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].CallID == callID {
			if s.rows[i].AnsweredAt == nil {
				t := answeredAt
				s.rows[i].AnsweredAt = &t
			}
			return nil
		}
	}
	return ErrNotFound
}

func (s *InMemoryStore) MarkEnded(ctx context.Context, callID uuid.UUID, endedAt time.Time, endReason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].CallID == callID {
			// First end event wins so racing hangups cannot corrupt the reason.
			if s.rows[i].EndedAt == nil {
				t := endedAt
				s.rows[i].EndedAt = &t
				s.rows[i].EndReason = endReason
			}
			return nil
		}
	}
	return ErrNotFound
}

func (s *InMemoryStore) ListForUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]Record, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var mine []Record
	for _, r := range s.rows {
		if r.CallerUserID == userID || r.CalleeUserID == userID {
			mine = append(mine, r)
		}
	}
	// Newest first (started_at DESC), stable by call_id for equal timestamps.
	sortRecords(mine)

	if s.users != nil {
		for i := range mine {
			if c, err := s.users.GetUserByID(ctx, mine[i].CallerUserID); err == nil {
				mine[i].CallerName = c.Name
				mine[i].CallerExtension = c.Extension
			}
			if c, err := s.users.GetUserByID(ctx, mine[i].CalleeUserID); err == nil {
				mine[i].CalleeName = c.Name
				mine[i].CalleeExtension = c.Extension
			}
		}
	}

	hasMore := offset+limit < len(mine)
	start := offset
	if start > len(mine) {
		start = len(mine)
	}
	end := start + limit
	if end > len(mine) {
		end = len(mine)
	}
	out := make([]Record, 0, end-start)
	for _, r := range mine[start:end] {
		out = append(out, r)
	}
	return out, hasMore, nil
}

// Records returns a copy of all stored rows (test helper).
func (s *InMemoryStore) Records(ctx context.Context) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Record, len(s.rows))
	copy(out, s.rows)
	return out
}

func sortRecords(rows []Record) {
	// insertion sort is fine for the tiny test store; Postgres uses the index.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			if rows[j].StartedAt.After(rows[j-1].StartedAt) ||
				(rows[j].StartedAt.Equal(rows[j-1].StartedAt) && rows[j].CallID.String() > rows[j-1].CallID.String()) {
				rows[j], rows[j-1] = rows[j-1], rows[j]
			} else {
				break
			}
		}
	}
}
