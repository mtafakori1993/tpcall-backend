package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"tpcall-backend/internal/auth"
	"tpcall-backend/internal/config"
	"tpcall-backend/internal/history"
	"tpcall-backend/internal/signaling"
	"tpcall-backend/internal/users"
)

// TestWebSocketThroughFullRouter ensures the WebSocket endpoint works through
// the complete chi + middleware + CORS chain, including http.Hijacker support
// that the status writer must preserve.
func TestWebSocketThroughFullRouter(t *testing.T) {
	cfg := &config.Config{CORSOrigins: []string{"*"}, JWTSecret: string(testJWTSecret)}
	store := users.NewInMemoryStore()
	svc := auth.NewService(store, testJWTSecret, time.Hour)
	hub := signaling.NewHub(store, history.NewInMemoryStore().WithUsers(store), slog.New(slog.NewTextHandler(io.Discard, nil)), testJWTSecret, time.Second)
	srv := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakePinger{}, store, history.NewInMemoryStore().WithUsers(store), svc, hub)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		hub.Shutdown(context.Background())
		ts.Close()
	})

	u, err := store.CreateUser(context.Background(), "Mohammad", "101", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tok, err := svc.TokenFor(u)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	wsURL := "ws" + ts.URL[4:] + "/api/v1/ws"
	header := http.Header{"Authorization": {"Bearer " + tok}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("dial failed: %v status=%d body=%s", err, resp.StatusCode, body)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	b, _ := json.Marshal(map[string]any{"type": "call", "payload": map[string]string{"to": "999"}})
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, data)
	}
	if env.Type != "error" {
		t.Fatalf("expected error message, got %q", env.Type)
	}
	var payload struct {
		Code string `json:"code"`
	}
	json.Unmarshal(env.Payload, &payload)
	if payload.Code != "user_not_found" {
		t.Fatalf("expected user_not_found, got %s", data)
	}
}

// TestWebSocketRequiresAuthThroughRouter verifies the 401 path.
func TestWebSocketRequiresAuthThroughRouter(t *testing.T) {
	cfg := &config.Config{CORSOrigins: []string{"*"}, JWTSecret: string(testJWTSecret)}
	store := users.NewInMemoryStore()
	svc := auth.NewService(store, testJWTSecret, time.Hour)
	hub := signaling.NewHub(store, history.NewInMemoryStore().WithUsers(store), slog.New(slog.NewTextHandler(io.Discard, nil)), testJWTSecret, time.Second)
	srv := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakePinger{}, store, history.NewInMemoryStore().WithUsers(store), svc, hub)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		hub.Shutdown(context.Background())
		ts.Close()
	})

	wsURL := "ws" + ts.URL[4:] + "/api/v1/ws"
	header := http.Header{"Authorization": {"Bearer garbage"}}
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("expected dial to fail with bad token")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
