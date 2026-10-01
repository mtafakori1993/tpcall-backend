package history

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCreateCallIsIdempotentPerCallID(t *testing.T) {
	s := NewInMemoryStore()
	callID := uuid.New()
	caller := uuid.New()
	callee := uuid.New()
	now := time.Now()

	if err := s.CreateCall(context.Background(), callID, caller, callee, now); err != nil {
		t.Fatalf("CreateCall: %v", err)
	}
	// A racing duplicate create must not add a second row.
	if err := s.CreateCall(context.Background(), callID, caller, callee, now.Add(time.Second)); err != nil {
		t.Fatalf("CreateCall duplicate: %v", err)
	}
	rows := s.Records(context.Background())
	if len(rows) != 1 {
		t.Fatalf("expected exactly one row, got %d", len(rows))
	}
}

// TestConcurrentDuplicateEndLeavesOneRow exercises the reject+timeout /
// hangup+disconnect races: many goroutines end the same call with different
// reasons; the store must keep one row with the first recorded reason.
func TestConcurrentDuplicateEndLeavesOneRow(t *testing.T) {
	s := NewInMemoryStore()
	callID := uuid.New()
	caller := uuid.New()
	callee := uuid.New()
	now := time.Now()
	if err := s.CreateCall(context.Background(), callID, caller, callee, now); err != nil {
		t.Fatalf("CreateCall: %v", err)
	}

	reasons := []string{EndReasonRejected, EndReasonNoAnswer, EndReasonCallerCancelled, EndReasonPeerDisconnected}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.MarkEnded(context.Background(), callID, now.Add(time.Duration(i)*time.Millisecond), reasons[i%len(reasons)])
		}(i)
	}
	wg.Wait()

	rows := s.Records(context.Background())
	if len(rows) != 1 {
		t.Fatalf("expected exactly one row after racing ends, got %d", len(rows))
	}
	if rows[0].EndedAt == nil {
		t.Fatal("ended_at must be set")
	}
	// The first end reason recorded wins; it must be one of the valid values.
	valid := map[string]bool{
		EndReasonRejected: true, EndReasonNoAnswer: true,
		EndReasonCallerCancelled: true, EndReasonPeerDisconnected: true,
	}
	if !valid[rows[0].EndReason] {
		t.Fatalf("unexpected end_reason %q", rows[0].EndReason)
	}
}

func TestMarkAnsweredDoesNotOverwrite(t *testing.T) {
	s := NewInMemoryStore()
	callID := uuid.New()
	if err := s.CreateCall(context.Background(), callID, uuid.New(), uuid.New(), time.Now()); err != nil {
		t.Fatalf("CreateCall: %v", err)
	}
	first := time.Now()
	second := first.Add(time.Minute)
	if err := s.MarkAnswered(context.Background(), callID, first); err != nil {
		t.Fatalf("MarkAnswered: %v", err)
	}
	if err := s.MarkAnswered(context.Background(), callID, second); err != nil {
		t.Fatalf("MarkAnswered duplicate: %v", err)
	}
	rows := s.Records(context.Background())
	if rows[0].AnsweredAt == nil || !rows[0].AnsweredAt.Equal(first) {
		t.Fatalf("answered_at must keep the first value, got %v", rows[0].AnsweredAt)
	}
}

func TestListForUserFiltersAndOrders(t *testing.T) {
	s := NewInMemoryStore()
	caller := uuid.New()
	callee := uuid.New()
	other := uuid.New()

	base := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	if err := s.CreateCall(context.Background(), uuid.New(), caller, callee, base); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateCall(context.Background(), uuid.New(), callee, caller, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateCall(context.Background(), uuid.New(), other, callee, base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// "other" participates in one call only.
	rows, hasMore, err := s.ListForUser(context.Background(), other, 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || hasMore {
		t.Fatalf("other: got %d rows (hasMore %v), want 1", len(rows), hasMore)
	}

	// caller sees two calls, newest first.
	rows, _, err = s.ListForUser(context.Background(), caller, 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("caller rows = %d, want 2", len(rows))
	}
	if !rows[0].StartedAt.After(rows[1].StartedAt) {
		t.Fatal("rows must be ordered newest-first")
	}
}
