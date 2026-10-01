package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tpcall-backend/internal/auth"
	"tpcall-backend/internal/config"
	"tpcall-backend/internal/history"
	"tpcall-backend/internal/signaling"
	"tpcall-backend/internal/users"
)

var testJWTSecret = []byte("test-jwt-secret-abcdefghijklmnop")

type fakePinger struct {
	err error
}

func (f fakePinger) Ping(ctx context.Context) error { return f.err }

type testServer struct {
	ts      *httptest.Server
	store   *users.InMemoryStore
	history *history.InMemoryStore
	auth    *auth.Service
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	cfg := &config.Config{
		CORSOrigins:     []string{"*"},
		JWTSecret:       string(testJWTSecret),
		ICEServers:      nil,
		AdminExtensions: []string{"101"},
	}
	store := users.NewInMemoryStore()
	callHistory := history.NewInMemoryStore().WithUsers(store)
	authSvc := auth.NewService(store, testJWTSecret, time.Hour)
	hub := signaling.NewHub(store, callHistory, slog.New(slog.NewTextHandler(io.Discard, nil)), testJWTSecret, time.Second)
	srv := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakePinger{}, store, callHistory, authSvc, hub)

	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		hub.Shutdown(context.Background())
	})
	return &testServer{ts: ts, store: store, history: callHistory, auth: authSvc}
}

func (s *testServer) post(t *testing.T, path string, body any, token string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, s.ts.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func (s *testServer) get(t *testing.T, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.ts.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func decodeBody[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode response %q: %v", data, err)
	}
	return v
}

func register(t *testing.T, s *testServer, name, ext, password string) string {
	t.Helper()
	resp, data := s.post(t, "/api/v1/auth/register", map[string]any{
		"name": name, "extension": ext, "password": password,
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s: status %d body %s", ext, resp.StatusCode, data)
	}
	return decodeBody[tokenResponse](t, data).AccessToken
}

func TestHealth(t *testing.T) {
	s := newTestServer(t)
	resp, data := s.get(t, "/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status %d body %s", resp.StatusCode, data)
	}
	if got := decodeBody[map[string]string](t, data); got["status"] != "ok" {
		t.Fatalf("unexpected health body: %s", data)
	}
}

func TestRegisterSuccess(t *testing.T) {
	s := newTestServer(t)
	resp, data := s.post(t, "/api/v1/auth/register", map[string]any{
		"name": "محمد تفکری", "extension": "101", "password": "password",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	var tok tokenResponse
	if err := json.Unmarshal(data, &tok); err != nil {
		t.Fatalf("decode: %v (%s)", err, data)
	}
	if tok.AccessToken == "" || tok.TokenType != "Bearer" {
		t.Fatalf("unexpected tokens: %+v", tok)
	}
	if tok.User.Extension != "101" || tok.User.Name != "محمد تفکری" {
		t.Fatalf("unexpected user: %+v", tok.User)
	}
}

func TestRegisterDuplicateExtension(t *testing.T) {
	s := newTestServer(t)
	register(t, s, "Alice", "101", "password")
	resp, data := s.post(t, "/api/v1/auth/register", map[string]any{
		"name": "Bob", "extension": "101", "password": "password",
	}, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	if got := decodeBody[httpxError](t, data); got.Error != "extension_already_exists" {
		t.Fatalf("unexpected error code: %s", data)
	}
}

func TestRegisterInvalidExtension(t *testing.T) {
	s := newTestServer(t)
	for _, ext := range []string{"001", "020", "099", "abc", "10", "1000", "12a"} {
		resp, data := s.post(t, "/api/v1/auth/register", map[string]any{
			"name": "User", "extension": ext, "password": "password",
		}, "")
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("extension %q: status %d body %s", ext, resp.StatusCode, data)
		}
	}
}

func TestRegisterValidExtension(t *testing.T) {
	s := newTestServer(t)
	for _, ext := range []string{"100", "101", "203", "999"} {
		resp, data := s.post(t, "/api/v1/auth/register", map[string]any{
			"name": "User", "extension": ext, "password": "password",
		}, "")
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("extension %q: status %d body %s", ext, resp.StatusCode, data)
		}
	}
}

func TestRegisterInvalidNameAndPassword(t *testing.T) {
	s := newTestServer(t)
	cases := []map[string]any{
		{"name": "", "extension": "101", "password": "password"},
		{"name": "a", "extension": "101", "password": "password"},
		{"name": "Valid", "extension": "101", "password": "123"},
		{"name": "Valid", "extension": "101", "password": ""},
	}
	for _, body := range cases {
		resp, data := s.post(t, "/api/v1/auth/register", body, "")
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %v: status %d body %s", body, resp.StatusCode, data)
		}
	}
}

func TestLoginSuccess(t *testing.T) {
	s := newTestServer(t)
	register(t, s, "Mohammad", "101", "password")
	resp, data := s.post(t, "/api/v1/auth/login", map[string]any{
		"extension": "101", "password": "password",
	}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	if tok := decodeBody[tokenResponse](t, data); tok.AccessToken == "" {
		t.Fatal("expected access token")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	s := newTestServer(t)
	register(t, s, "Mohammad", "101", "password")
	resp, data := s.post(t, "/api/v1/auth/login", map[string]any{
		"extension": "101", "password": "wrong-pass",
	}, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	if got := decodeBody[httpxError](t, data); got.Error != "invalid_credentials" {
		t.Fatalf("unexpected error code: %s", data)
	}
}

func TestLoginUnknownExtension(t *testing.T) {
	s := newTestServer(t)
	resp, data := s.post(t, "/api/v1/auth/login", map[string]any{
		"extension": "404", "password": "password",
	}, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	if got := decodeBody[httpxError](t, data); got.Error != "invalid_credentials" {
		t.Fatalf("unexpected error code: %s", data)
	}
}

func TestMeRequiresAuth(t *testing.T) {
	s := newTestServer(t)
	resp, data := s.get(t, "/api/v1/me", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
}

func TestMeWithValidToken(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "Mohammad", "101", "password")
	resp, data := s.get(t, "/api/v1/me", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	me := decodeBody[meResponse](t, data)
	if me.Extension != "101" || me.Name != "Mohammad" {
		t.Fatalf("unexpected me: %+v", me)
	}
	if me.Online {
		t.Fatal("expected online=false with no websocket connected")
	}
}

func TestGetUser(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "حسابداری", "203", "password")
	resp, data := s.get(t, "/api/v1/users/203", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	entry := decodeBody[directoryEntry](t, data)
	if entry.Extension != "203" || entry.Name != "حسابداری" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestGetUserNotFound(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "Alice", "101", "password")
	resp, data := s.get(t, "/api/v1/users/999", token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	if got := decodeBody[httpxError](t, data); got.Error != "user_not_found" {
		t.Fatalf("unexpected error code: %s", data)
	}
}

func TestListUsers(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "Mohammad", "101", "password")
	register(t, s, "Hesabdari", "203", "password")
	resp, data := s.get(t, "/api/v1/users", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	entries := decodeBody[[]directoryEntry](t, data)
	if len(entries) != 2 {
		t.Fatalf("expected 2 users, got %d (%s)", len(entries), data)
	}
	if entries[0].Extension != "101" || entries[1].Extension != "203" {
		t.Fatalf("users not sorted by extension: %s", data)
	}
}

func TestWebRTCConfig(t *testing.T) {
	s := newTestServer(t)
	token := register(t, s, "Alice", "101", "password")
	resp, data := s.get(t, "/api/v1/webrtc/config", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, data)
	}
	var cfg struct {
		IceServers []any `json:"iceServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.IceServers == nil {
		t.Fatalf("iceServers must be an array: %s", data)
	}
}

func TestHealthWhenDBDown(t *testing.T) {
	cfg := &config.Config{CORSOrigins: []string{"*"}, JWTSecret: string(testJWTSecret)}
	store := users.NewInMemoryStore()
	callHistory := history.NewInMemoryStore().WithUsers(store)
	authSvc := auth.NewService(store, testJWTSecret, time.Hour)
	hub := signaling.NewHub(store, callHistory, slog.New(slog.NewTextHandler(io.Discard, nil)), testJWTSecret, time.Second)
	srv := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakePinger{err: errors.New("db down")}, store, callHistory, authSvc, hub)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		hub.Shutdown(context.Background())
	})
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

type httpxError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}
