package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"

	"tpcall-backend/internal/auth"
	"tpcall-backend/internal/config"
	"tpcall-backend/internal/history"
	"tpcall-backend/internal/middleware"
	"tpcall-backend/internal/signaling"
	"tpcall-backend/internal/users"
)

// DBPinger is the health-check dependency for the database.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// Server wires the REST handlers and the WebSocket hub onto a single router.
type Server struct {
	cfg     *config.Config
	logger  *slog.Logger
	db      DBPinger
	users   users.Store
	history history.Store
	auth    *auth.Service
	hub     *signaling.Hub
}

// NewServer builds the HTTP server.
func NewServer(cfg *config.Config, logger *slog.Logger, db DBPinger, store users.Store, callHistory history.Store, authSvc *auth.Service, hub *signaling.Hub) *Server {
	return &Server{cfg: cfg, logger: logger, db: db, users: store, history: callHistory, auth: authSvc, hub: hub}
}

// Routes assembles the full middleware and route tree.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(s.logger))
	r.Use(middleware.Recoverer(s.logger))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: s.cfg.CORSOrigins,
		AllowedMethods: []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders: []string{"X-Request-ID"},
		MaxAge:         300,
	}))

	r.Get("/health", s.handleHealth)

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/register", s.handleRegister)
		r.Post("/auth/login", s.handleLogin)

		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticator([]byte(s.cfg.JWTSecret), s.users))
			r.Get("/me", s.handleMe)
			r.Get("/users", s.handleListUsers)
			r.Get("/users/{extension}", s.handleGetUser)
			r.Get("/webrtc/config", s.handleWebRTCConfig)
			r.Get("/calls", s.handleListCalls)
		})

		// The WebSocket endpoint performs its own authentication (header or
		// query token) so it is deliberately outside the REST auth group.
		r.Get("/ws", s.hub.ServeWS)
	})

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		s.logger.Warn("health_check_failed", "error", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"error":  "database_unreachable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
