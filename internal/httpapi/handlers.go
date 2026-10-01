package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"tpcall-backend/internal/auth"
	"tpcall-backend/internal/httpx"
	"tpcall-backend/internal/middleware"
	"tpcall-backend/internal/users"
)

type registerRequest struct {
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Password  string `json:"password"`
}

type loginRequest struct {
	Extension string `json:"extension"`
	Password  string `json:"password"`
}

type tokenResponse struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	User        tokenUser `json:"user"`
}

type tokenUser struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Extension string    `json:"extension"`
}

type meResponse struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Extension string    `json:"extension"`
	Online    bool      `json:"online"`
}

type directoryEntry struct {
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Online    bool   `json:"online"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized JSON body")
		return
	}

	name := strings.TrimSpace(req.Name)
	if n := utf8.RuneCountInString(name); n < 2 || n > 100 {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "name must be between 2 and 100 characters")
		return
	}
	if !users.ValidExtension(req.Extension) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "extension must be three digits between 100 and 999")
		return
	}
	if len(req.Password) < 6 || len(req.Password) > 128 {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "password must be between 6 and 128 characters")
		return
	}

	u, tokens, err := s.auth.Register(r.Context(), name, req.Extension, req.Password)
	if errors.Is(err, users.ErrExtensionExists) {
		httpx.WriteError(w, http.StatusConflict, "extension_already_exists", "extension already exists")
		return
	}
	if err != nil {
		s.logger.Error("register_failed", "request_id", middleware.RequestIDFromContext(r.Context()), "error", err.Error())
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not create user")
		return
	}

	s.logger.Info("user_registered", "extension", u.Extension, "user_id", u.ID.String())
	writeJSON(w, http.StatusCreated, tokenResponse{
		AccessToken: tokens.AccessToken,
		TokenType:   tokens.TokenType,
		User: tokenUser{
			ID:        u.ID,
			Name:      u.Name,
			Extension: u.Extension,
		},
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized JSON body")
		return
	}

	u, tokens, err := s.auth.Login(r.Context(), req.Extension, req.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		s.logger.Info("login_failed", "extension", req.Extension)
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "invalid extension or password")
		return
	}
	if err != nil {
		s.logger.Error("login_failed", "extension", req.Extension, "error", err.Error())
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not log in")
		return
	}

	s.logger.Info("login_success", "extension", u.Extension, "user_id", u.ID.String())
	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken: tokens.AccessToken,
		TokenType:   tokens.TokenType,
		User: tokenUser{
			ID:        u.ID,
			Name:      u.Name,
			Extension: u.Extension,
		},
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	au, ok := middleware.UserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, meResponse{
		ID:        au.ID,
		Name:      au.Name,
		Extension: au.Extension,
		Online:    s.hub.IsOnline(au.Extension),
	})
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	extension := chi.URLParam(r, "extension")
	if !users.ValidExtension(extension) {
		httpx.WriteError(w, http.StatusNotFound, "user_not_found", "user not found")
		return
	}
	u, err := s.users.GetUserByExtension(r.Context(), extension)
	if errors.Is(err, users.ErrUserNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "user_not_found", "user not found")
		return
	}
	if err != nil {
		s.logger.Error("get_user_failed", "extension", extension, "error", err.Error())
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, directoryEntry{
		Name:      u.Name,
		Extension: u.Extension,
		Online:    s.hub.IsOnline(u.Extension),
	})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	all, err := s.users.ListUsers(r.Context())
	if err != nil {
		s.logger.Error("list_users_failed", "error", err.Error())
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Extension < all[j].Extension })
	entries := make([]directoryEntry, 0, len(all))
	for _, u := range all {
		entries = append(entries, directoryEntry{
			Name:      u.Name,
			Extension: u.Extension,
			Online:    s.hub.IsOnline(u.Extension),
		})
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleWebRTCConfig(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.ICEServers) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"iceServers": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iceServers": []map[string]any{{"urls": s.cfg.ICEServers}},
	})
}
