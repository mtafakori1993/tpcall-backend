package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"tpcall-backend/internal/history"
	"tpcall-backend/internal/httpx"
	"tpcall-backend/internal/middleware"
)

const (
	historyDefaultLimit = 30
	historyMaxLimit     = 100
)

// callHistoryPeer is the normalized peer reference in a history item.
type callHistoryPeer struct {
	Name      string `json:"name"`
	Extension string `json:"extension"`
}

// callHistoryItem is an Android-friendly normalized history record. Direction,
// peer, and status are derived here from the authenticated user's identity —
// the client never supplies a user id.
type callHistoryItem struct {
	ID              string          `json:"id"`
	CallID          string          `json:"call_id"`
	Direction       string          `json:"direction"`
	Status          string          `json:"status"`
	Peer            callHistoryPeer `json:"peer"`
	StartedAt       time.Time       `json:"started_at"`
	AnsweredAt      *time.Time      `json:"answered_at"`
	EndedAt         *time.Time      `json:"ended_at"`
	DurationSeconds *int64          `json:"duration_seconds"`
}

type callHistoryResponse struct {
	Calls   []callHistoryItem `json:"calls"`
	Limit   int               `json:"limit"`
	Offset  int               `json:"offset"`
	HasMore bool              `json:"has_more"`
}

// handleListCalls serves GET /api/v1/calls. Identity comes exclusively from
// the JWT; the SQL always filters by the authenticated user id, so changing
// query parameters can never leak another employee's history.
func (s *Server) handleListCalls(w http.ResponseWriter, r *http.Request) {
	au, ok := middleware.UserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	limit, offset, err := parsePagination(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}

	records, hasMore, err := s.history.ListForUser(r.Context(), au.ID, limit, offset)
	if err != nil {
		s.logger.Error("call_history_query",
			"request_id", middleware.RequestIDFromContext(r.Context()),
			"error", err.Error(),
		)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not load call history")
		return
	}

	s.logger.Info("call_history_query",
		"request_id", middleware.RequestIDFromContext(r.Context()),
		"user_id", au.ID.String(),
		"limit", limit,
		"offset", offset,
		"rows", len(records),
		"has_more", hasMore,
	)

	calls := make([]callHistoryItem, 0, len(records))
	for _, rec := range records {
		calls = append(calls, normalizeCallHistoryItem(rec, au.ID))
	}

	writeJSON(w, http.StatusOK, callHistoryResponse{
		Calls:   calls,
		Limit:   limit,
		Offset:  offset,
		HasMore: hasMore,
	})
}

func normalizeCallHistoryItem(rec history.Record, userID uuid.UUID) callHistoryItem {
	item := callHistoryItem{
		ID:         rec.ID.String(),
		CallID:     rec.CallID.String(),
		StartedAt:  rec.StartedAt.UTC(),
		AnsweredAt: utcTime(rec.AnsweredAt),
		EndedAt:    utcTime(rec.EndedAt),
	}

	if rec.CallerUserID == userID {
		item.Direction = "outgoing"
		item.Peer = callHistoryPeer{Name: rec.CalleeName, Extension: rec.CalleeExtension}
	} else {
		item.Direction = "incoming"
		item.Peer = callHistoryPeer{Name: rec.CallerName, Extension: rec.CallerExtension}
	}
	item.Status = deriveStatus(rec)

	if rec.AnsweredAt != nil && rec.EndedAt != nil {
		d := int64(rec.EndedAt.Sub(*rec.AnsweredAt).Seconds())
		item.DurationSeconds = &d
	}

	return item
}

// deriveStatus maps the persisted end state to the stable API status enum.
// Answered calls are always "completed"; a ringing call declined by the callee
// (explicit reject or callee cancel) is "rejected"; everything else that never
// connected is "missed".
func deriveStatus(rec history.Record) string {
	if rec.AnsweredAt != nil {
		return "completed"
	}
	switch rec.EndReason {
	case history.EndReasonRejected, history.EndReasonCalleeCancelled:
		return "rejected"
	default:
		return "missed"
	}
}

func utcTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func parsePagination(r *http.Request) (limit, offset int, err error) {
	limit = historyDefaultLimit
	offset = 0

	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return 0, 0, errors.New("limit must be a positive integer")
		}
		if limit > historyMaxLimit {
			limit = historyMaxLimit
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}
