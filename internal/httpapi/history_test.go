package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"tpcall-backend/internal/history"
)

func userID(t *testing.T, s *testServer, ext string) uuid.UUID {
	t.Helper()
	u, err := s.store.GetUserByExtension(context.Background(), ext)
	if err != nil {
		t.Fatalf("user %s: %v", ext, err)
	}
	return u.ID
}

// seedCall inserts a complete history record with the given end reason.
// answered controls whether the call was accepted; answeredAt/endedAt control
// the exact timestamps so duration and ordering tests are deterministic.
func (s *testServer) seedCall(t *testing.T, caller, callee uuid.UUID, started time.Time, answered bool, endReason string, ended time.Time) string {
	t.Helper()
	callID := uuid.New()
	if err := s.history.CreateCall(context.Background(), callID, caller, callee, started); err != nil {
		t.Fatalf("seed call: %v", err)
	}
	if answered {
		if err := s.history.MarkAnswered(context.Background(), callID, started.Add(8*time.Second)); err != nil {
			t.Fatalf("seed answered: %v", err)
		}
	}
	if err := s.history.MarkEnded(context.Background(), callID, ended, endReason); err != nil {
		t.Fatalf("seed end: %v", err)
	}
	return callID.String()
}

type historyTestItem struct {
	ID              string   `json:"id"`
	CallID          string   `json:"call_id"`
	Direction       string   `json:"direction"`
	Status          string   `json:"status"`
	Peer            peerTest `json:"peer"`
	StartedAt       string   `json:"started_at"`
	AnsweredAt      *string  `json:"answered_at"`
	EndedAt         *string  `json:"ended_at"`
	DurationSeconds *int64   `json:"duration_seconds"`
}

type peerTest struct {
	Name      string `json:"name"`
	Extension string `json:"extension"`
}

type historyTestResponse struct {
	Calls   []historyTestItem `json:"calls"`
	Limit   int               `json:"limit"`
	Offset  int               `json:"offset"`
	HasMore bool              `json:"has_more"`
}

func getHistory(t *testing.T, s *testServer, token, query string) (*http.Response, historyTestResponse) {
	t.Helper()
	path := "/api/v1/calls"
	if query != "" {
		path += "?" + query
	}
	resp, data := s.get(t, path, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get calls: status %d body %s", resp.StatusCode, data)
	}
	return resp, decodeBody[historyTestResponse](t, data)
}

func TestCallHistoryRequiresAuth(t *testing.T) {
	s := newTestServer(t)
	resp, _ := s.get(t, "/api/v1/calls", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}
}

func TestCallHistoryEmptyForNewUser(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "محمد تفکری", "101", "password")
	resp, data := s.get(t, "/api/v1/calls", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	got := decodeBody[historyTestResponse](t, data)
	if len(got.Calls) != 0 || got.HasMore || got.Limit != 30 || got.Offset != 0 {
		t.Fatalf("unexpected empty response: %+v", got)
	}
}

func TestCallHistoryDirectionAndPeerNormalization(t *testing.T) {
	s := newTestServer(t)
	t101 := register(t, s, "محمد تفکری", "101", "password")
	t203 := register(t, s, "حسابداری", "203", "password")

	started := time.Date(2026, 8, 19, 14, 30, 0, 0, time.UTC)
	s.seedCall(t, userID(t, s, "101"), userID(t, s, "203"), started, true, history.EndReasonCompleted, started.Add(2*time.Minute+18*time.Second))

	// Caller's view: outgoing, peer = callee.
	_, callerView := getHistory(t, s, t101, "")
	if len(callerView.Calls) != 1 {
		t.Fatalf("caller view rows = %d", len(callerView.Calls))
	}
	callerItem := callerView.Calls[0]
	if callerItem.Direction != "outgoing" {
		t.Fatalf("caller direction = %q, want outgoing", callerItem.Direction)
	}
	if callerItem.Peer.Name != "حسابداری" || callerItem.Peer.Extension != "203" {
		t.Fatalf("caller peer = %+v, want حسابداری/203", callerItem.Peer)
	}

	// Callee's view: incoming, peer = caller.
	_, calleeView := getHistory(t, s, t203, "")
	if len(calleeView.Calls) != 1 {
		t.Fatalf("callee view rows = %d", len(calleeView.Calls))
	}
	calleeItem := calleeView.Calls[0]
	if calleeItem.Direction != "incoming" {
		t.Fatalf("callee direction = %q, want incoming", calleeItem.Direction)
	}
	if calleeItem.Peer.Name != "محمد تفکری" || calleeItem.Peer.Extension != "101" {
		t.Fatalf("callee peer = %+v, want محمد تفکری/101", calleeItem.Peer)
	}

	// The two views share the same underlying call_id.
	if callerItem.CallID != calleeItem.CallID {
		t.Fatal("both participants must reference the same call record")
	}
}

func TestCallHistoryDurationComputedFromAnsweredAt(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "محمد تفکری", "101", "password")
	register(t, s, "حسابداری", "203", "password")

	started := time.Date(2026, 8, 19, 14, 30, 0, 0, time.UTC)
	// started 14:30:00, answered 14:30:08, ended 14:32:26 -> duration 138s.
	s.seedCall(t, userID(t, s, "101"), userID(t, s, "203"), started, true, history.EndReasonCompleted, started.Add(2*time.Minute+26*time.Second))

	_, view := getHistory(t, s, token, "")
	if len(view.Calls) != 1 {
		t.Fatalf("rows = %d", len(view.Calls))
	}
	item := view.Calls[0]
	if item.DurationSeconds == nil || *item.DurationSeconds != 138 {
		t.Fatalf("duration = %v, want 138", item.DurationSeconds)
	}
	if item.AnsweredAt == nil || item.EndedAt == nil {
		t.Fatal("answered_at and ended_at must be present for a completed call")
	}
}

func TestCallHistoryMissedAndRejectedStatus(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "محمد تفکری", "101", "password")
	register(t, s, "حسابداری", "203", "password")

	started := time.Date(2026, 8, 18, 17, 45, 0, 0, time.UTC)
	s.seedCall(t, userID(t, s, "101"), userID(t, s, "203"), started, false, history.EndReasonNoAnswer, started.Add(30*time.Second))
	s.seedCall(t, userID(t, s, "101"), userID(t, s, "203"), started.Add(-time.Hour), false, history.EndReasonRejected, started.Add(-time.Hour+5*time.Second))

	_, view := getHistory(t, s, token, "")
	if len(view.Calls) != 2 {
		t.Fatalf("rows = %d, want 2", len(view.Calls))
	}
	// Newest first: the no_answer call is first.
	if view.Calls[0].Status != "missed" {
		t.Fatalf("status[0] = %q, want missed", view.Calls[0].Status)
	}
	if view.Calls[0].DurationSeconds != nil {
		t.Fatalf("missed call duration must be null, got %v", view.Calls[0].DurationSeconds)
	}
	if view.Calls[1].Status != "rejected" {
		t.Fatalf("status[1] = %q, want rejected", view.Calls[1].Status)
	}
}

func TestCallHistoryNewestFirst(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "محمد تفکری", "101", "password")
	register(t, s, "حسابداری", "203", "password")

	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	ids := map[string]string{}
	ids["first"] = s.seedCall(t, userID(t, s, "101"), userID(t, s, "203"), base.Add(-2*time.Hour), true, history.EndReasonCompleted, base.Add(-2*time.Hour+time.Minute))
	ids["second"] = s.seedCall(t, userID(t, s, "101"), userID(t, s, "203"), base.Add(-time.Hour), true, history.EndReasonCompleted, base.Add(-time.Hour+time.Minute))
	ids["third"] = s.seedCall(t, userID(t, s, "101"), userID(t, s, "203"), base, true, history.EndReasonCompleted, base.Add(time.Minute))

	_, view := getHistory(t, s, token, "")
	if len(view.Calls) != 3 {
		t.Fatalf("rows = %d, want 3", len(view.Calls))
	}
	if view.Calls[0].CallID != ids["third"] || view.Calls[1].CallID != ids["second"] || view.Calls[2].CallID != ids["first"] {
		t.Fatal("history must be ordered newest-first")
	}
}

func TestCallHistoryPagination(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "محمد تفکری", "101", "password")
	register(t, s, "حسابداری", "203", "password")

	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 5; i++ {
		started := base.Add(-time.Duration(i) * time.Minute)
		ids = append(ids, s.seedCall(t, userID(t, s, "101"), userID(t, s, "203"), started, true, history.EndReasonCompleted, started.Add(time.Minute)))
	}

	// Page 1: limit=2, offset=0 -> two newest, has_more=true.
	_, page1 := getHistory(t, s, token, "limit=2&offset=0")
	if len(page1.Calls) != 2 || !page1.HasMore || page1.Limit != 2 || page1.Offset != 0 {
		t.Fatalf("page1 = %+v", page1)
	}
	if page1.Calls[0].CallID != ids[0] || page1.Calls[1].CallID != ids[1] {
		t.Fatal("page1 must return the two newest calls")
	}

	// Page 2: offset=2 -> next two, still has_more.
	_, page2 := getHistory(t, s, token, "limit=2&offset=2")
	if len(page2.Calls) != 2 || !page2.HasMore {
		t.Fatalf("page2 = %+v", page2)
	}
	if page2.Calls[0].CallID != ids[2] || page2.Calls[1].CallID != ids[3] {
		t.Fatal("page2 must return the next two calls")
	}

	// Page 3: offset=4 -> one remaining, has_more=false.
	_, page3 := getHistory(t, s, token, "limit=2&offset=4")
	if len(page3.Calls) != 1 || page3.HasMore {
		t.Fatalf("page3 = %+v", page3)
	}
	if page3.Calls[0].CallID != ids[4] {
		t.Fatal("page3 must return the oldest call")
	}

	// Defaults apply when no query parameters are given.
	_, def := getHistory(t, s, token, "")
	if def.Limit != 30 || def.Offset != 0 || len(def.Calls) != 5 || def.HasMore {
		t.Fatalf("defaults not applied: %+v", def)
	}
}

func TestCallHistoryLimitCapped(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "محمد تفکری", "101", "password")
	register(t, s, "حسابداری", "203", "password")

	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		started := base.Add(-time.Duration(i) * time.Minute)
		s.seedCall(t, userID(t, s, "101"), userID(t, s, "203"), started, true, history.EndReasonCompleted, started.Add(time.Minute))
	}

	// A huge requested limit is capped at 100 and still returns everything.
	_, view := getHistory(t, s, token, "limit=9999")
	if view.Limit != 100 || len(view.Calls) != 3 {
		t.Fatalf("limit cap not applied: %+v", view)
	}
}

func TestCallHistoryInvalidQuery(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "محمد تفکری", "101", "password")
	for _, q := range []string{"limit=abc", "limit=-5", "offset=-1"} {
		resp, data := s.get(t, "/api/v1/calls?"+q, token)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("query %q: status %d body %s", q, resp.StatusCode, data)
		}
	}
}

// The critical isolation test: 101 must never see history that belongs only to
// 203 and 305, regardless of query parameters.
func TestCallHistoryJWTIsolation(t *testing.T) {
	s := newTestServer(t)
	t101 := register(t, s, "محمد تفکری", "101", "password")
	t203 := register(t, s, "حسابداری", "203", "password")
	t305 := register(t, s, "رضا احمدی", "305", "password")

	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	// Calls between 203 and 305 only; 101 is not involved in any of them.
	s.seedCall(t, userID(t, s, "203"), userID(t, s, "305"), base.Add(-time.Minute), true, history.EndReasonCompleted, base)
	s.seedCall(t, userID(t, s, "305"), userID(t, s, "203"), base.Add(-2*time.Minute), false, history.EndReasonRejected, base.Add(-time.Minute))

	// 101 must see an empty history even when paging deep into the dataset.
	for _, q := range []string{"", "limit=100&offset=0", "limit=1&offset=10"} {
		_, view := getHistory(t, s, t101, q)
		if len(view.Calls) != 0 || view.HasMore {
			t.Fatalf("101 leaked history for query %q: %+v", q, view)
		}
	}

	// 203 and 305 each see both calls (they are participants).
	for _, token := range []string{t203, t305} {
		_, view := getHistory(t, s, token, "")
		if len(view.Calls) != 2 {
			t.Fatalf("participant history rows = %d, want 2", len(view.Calls))
		}
	}

	// No record exposes sensitive signaling internals.
	resp, data := s.get(t, "/api/v1/calls", t203)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, forbidden := range []string{"password", "sdp", "ice", "token", "remote_addr", "password_hash"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("response must not contain %q", forbidden)
		}
	}
}
