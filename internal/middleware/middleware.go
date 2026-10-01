package middleware

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"

	"tpcall-backend/internal/auth"
	"tpcall-backend/internal/httpx"
	"tpcall-backend/internal/users"
)

type ctxKey string

const (
	requestIDKey ctxKey = "request_id"
	userKey      ctxKey = "auth_user"
)

// AuthUser is the authenticated identity stored in the request context.
type AuthUser struct {
	ID        uuid.UUID
	Extension string
	Name      string
}

// RequestID sets or preserves an X-Request-ID header and stores it in context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if rid == "" {
			rid = uuid.NewString()
		}
		ctx := context.WithValue(r.Context(), requestIDKey, rid)
		w.Header().Set("X-Request-ID", rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the request ID for logging.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// Logger logs one structured line per HTTP request.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFromContext(r.Context()),
				"remote_addr", r.RemoteAddr,
			)
		})
	}
}

// Recoverer converts panics into 500 responses and logs the stack.
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic_recovered",
						"request_id", RequestIDFromContext(r.Context()),
						"error", fmt.Sprint(rec),
						"stack", string(debug.Stack()),
					)
					httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Authenticator requires a valid Bearer JWT and resolves the user.
func Authenticator(jwtSecret []byte, store users.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			const prefix = "Bearer "
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, prefix) {
				unauthorized(w)
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
			if token == "" {
				unauthorized(w)
				return
			}
			claims, err := auth.ParseToken(jwtSecret, token)
			if err != nil {
				unauthorized(w)
				return
			}
			id, err := uuid.Parse(claims.Subject)
			if err != nil {
				unauthorized(w)
				return
			}
			u, err := store.GetUserByID(r.Context(), id)
			if err != nil {
				unauthorized(w)
				return
			}
			au := AuthUser{ID: u.ID, Extension: u.Extension, Name: u.Name}
			ctx := context.WithValue(r.Context(), userKey, au)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext returns the authenticated user, if any.
func UserFromContext(ctx context.Context) (AuthUser, bool) {
	au, ok := ctx.Value(userKey).(AuthUser)
	return au, ok
}

func unauthorized(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack lets the WebSocket upgrader take over the connection. The wrapper must
// preserve the Hijacker behavior of the underlying writer.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijacking not supported")
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
