package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"tpcall-backend/internal/auth"
	"tpcall-backend/internal/history"
	"tpcall-backend/internal/httpx"
	"tpcall-backend/internal/middleware"
	"tpcall-backend/internal/users"
)

// requireAdmin allows only extensions listed in ADMIN_EXTENSIONS. It must run
// after the Authenticator middleware.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		au, ok := middleware.UserFromContext(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if !s.isAdmin(au.Extension) {
			httpx.WriteError(w, http.StatusForbidden, "forbidden", "administrator access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) isAdmin(extension string) bool {
	for _, e := range s.cfg.AdminExtensions {
		if e == extension {
			return true
		}
	}
	return false
}

type adminUser struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Extension   string     `json:"extension"`
	Online      bool       `json:"online"`
	IsAdmin     bool       `json:"is_admin"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

func (s *Server) toAdminUser(u users.User) adminUser {
	return adminUser{
		ID:          u.ID.String(),
		Name:        u.Name,
		Extension:   u.Extension,
		Online:      s.hub.IsOnline(u.Extension),
		IsAdmin:     s.isAdmin(u.Extension),
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

// handleAdminMe lets the dashboard check whether the logged-in user is an admin
// without triggering a 403 on a data endpoint.
func (s *Server) handleAdminMe(w http.ResponseWriter, r *http.Request) {
	au, _ := middleware.UserFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"name": au.Name, "extension": au.Extension, "is_admin": s.isAdmin(au.Extension),
	})
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	list, err := s.users.ListUsers(r.Context())
	if err != nil {
		s.adminError(w, r, "admin_stats", err)
		return
	}
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	st, err := s.history.Stats(r.Context(), startOfDay)
	if err != nil {
		s.adminError(w, r, "admin_stats", err)
		return
	}
	var avg int64
	if st.Answered > 0 {
		avg = st.TalkSecondsTotal / st.Answered
	}
	if st.ByReason == nil {
		st.ByReason = map[string]int64{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users":               len(list),
		"online":              s.hub.OnlineCount(),
		"calls_total":         st.CallsTotal,
		"calls_today":         st.CallsSince,
		"calls_answered":      st.Answered,
		"talk_seconds_total":  st.TalkSecondsTotal,
		"avg_talk_seconds":    avg,
		"calls_by_end_reason": st.ByReason,
	})
}

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.users.ListUsers(r.Context())
	if err != nil {
		s.adminError(w, r, "admin_list_users", err)
		return
	}
	out := make([]adminUser, 0, len(list))
	for _, u := range list {
		out = append(out, s.toAdminUser(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

type adminUserRequest struct {
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Password  string `json:"password"`
}

func validName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n >= 2 && n <= 100
}

func validPassword(p string) bool { return len(p) >= 6 && len(p) <= 128 }

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req adminUserRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case !validName(req.Name):
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "name must be between 2 and 100 characters")
		return
	case !users.ValidExtension(req.Extension):
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "extension must be three digits between 100 and 999")
		return
	case !validPassword(req.Password):
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "password must be between 6 and 128 characters")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.adminError(w, r, "admin_create_user", err)
		return
	}
	u, err := s.users.CreateUser(r.Context(), req.Name, req.Extension, hash)
	if errors.Is(err, users.ErrExtensionExists) {
		httpx.WriteError(w, http.StatusConflict, "extension_already_exists", "extension already exists")
		return
	}
	if err != nil {
		s.adminError(w, r, "admin_create_user", err)
		return
	}
	s.auditAdmin(r, "user_created", u.Extension)
	writeJSON(w, http.StatusCreated, s.toAdminUser(u))
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.adminTargetUser(w, r)
	if !ok {
		return
	}
	var req adminUserRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validName(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "name must be between 2 and 100 characters")
		return
	}
	if err := s.users.UpdateName(r.Context(), u.ID, req.Name); err != nil {
		s.adminError(w, r, "admin_update_user", err)
		return
	}
	u.Name = req.Name
	s.auditAdmin(r, "user_renamed", u.Extension)
	writeJSON(w, http.StatusOK, s.toAdminUser(u))
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := s.adminTargetUser(w, r)
	if !ok {
		return
	}
	var req adminUserRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized JSON body")
		return
	}
	if !validPassword(req.Password) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "password must be between 6 and 128 characters")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.adminError(w, r, "admin_reset_password", err)
		return
	}
	if err := s.users.UpdatePasswordHash(r.Context(), u.ID, hash); err != nil {
		s.adminError(w, r, "admin_reset_password", err)
		return
	}
	s.hub.Disconnect(u.Extension) // force the user to log in again with the new password
	s.auditAdmin(r, "password_reset", u.Extension)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.adminTargetUser(w, r)
	if !ok {
		return
	}
	if au, _ := middleware.UserFromContext(r.Context()); au.ID == u.ID {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "you cannot delete your own account")
		return
	}
	if err := s.users.DeleteUser(r.Context(), u.ID); err != nil {
		s.adminError(w, r, "admin_delete_user", err)
		return
	}
	s.hub.Disconnect(u.Extension)
	s.auditAdmin(r, "user_deleted", u.Extension)
	w.WriteHeader(http.StatusNoContent)
}

type adminCall struct {
	CallID          string     `json:"call_id"`
	CallerName      string     `json:"caller_name"`
	CallerExtension string     `json:"caller_extension"`
	CalleeName      string     `json:"callee_name"`
	CalleeExtension string     `json:"callee_extension"`
	StartedAt       time.Time  `json:"started_at"`
	AnsweredAt      *time.Time `json:"answered_at"`
	EndedAt         *time.Time `json:"ended_at"`
	DurationSeconds *int64     `json:"duration_seconds"`
	EndReason       string     `json:"end_reason"`
}

func (s *Server) handleAdminListCalls(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parsePagination(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	recs, hasMore, err := s.history.ListAll(r.Context(), limit, offset)
	if err != nil {
		s.adminError(w, r, "admin_list_calls", err)
		return
	}
	out := make([]adminCall, 0, len(recs))
	for _, rec := range recs {
		out = append(out, toAdminCall(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": out, "limit": limit, "offset": offset, "has_more": hasMore})
}

func toAdminCall(rec history.Record) adminCall {
	c := adminCall{
		CallID: rec.CallID.String(), CallerName: rec.CallerName, CallerExtension: rec.CallerExtension,
		CalleeName: rec.CalleeName, CalleeExtension: rec.CalleeExtension,
		StartedAt: rec.StartedAt, AnsweredAt: rec.AnsweredAt, EndedAt: rec.EndedAt, EndReason: rec.EndReason,
	}
	if rec.AnsweredAt != nil && rec.EndedAt != nil {
		d := int64(rec.EndedAt.Sub(*rec.AnsweredAt).Seconds())
		if d < 0 {
			d = 0
		}
		c.DurationSeconds = &d
	}
	return c
}

func (s *Server) adminTargetUser(w http.ResponseWriter, r *http.Request) (users.User, bool) {
	ext := chi.URLParam(r, "extension")
	if !users.ValidExtension(ext) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "extension must be three digits between 100 and 999")
		return users.User{}, false
	}
	u, err := s.users.GetUserByExtension(r.Context(), ext)
	if errors.Is(err, users.ErrUserNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "user_not_found", "user not found")
		return users.User{}, false
	}
	if err != nil {
		s.adminError(w, r, "admin_get_user", err)
		return users.User{}, false
	}
	return u, true
}

func (s *Server) adminError(w http.ResponseWriter, r *http.Request, event string, err error) {
	s.logger.Error(event, "request_id", middleware.RequestIDFromContext(r.Context()), "error", err.Error())
	httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "internal error")
}

func (s *Server) auditAdmin(r *http.Request, action, targetExtension string) {
	au, _ := middleware.UserFromContext(r.Context())
	s.logger.Info("admin_action", "action", action, "admin_extension", au.Extension, "target_extension", targetExtension)
}
