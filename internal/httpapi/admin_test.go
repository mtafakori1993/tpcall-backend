package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func (s *testServer) do(t *testing.T, method, path string, body any, token string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.ts.URL+path, reader)
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

func TestAdminRequiresAdminExtension(t *testing.T) {
	s := newTestServer(t)
	admin := register(t, s, "Admin", "101", "password")
	user := register(t, s, "User", "102", "password")

	if resp, _ := s.get(t, "/api/v1/admin/users", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: got %d", resp.StatusCode)
	}
	if resp, _ := s.get(t, "/api/v1/admin/users", user); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin: got %d", resp.StatusCode)
	}
	if resp, data := s.get(t, "/api/v1/admin/users", admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin: got %d %s", resp.StatusCode, data)
	}

	_, data := s.get(t, "/api/v1/admin/me", user)
	if me := decodeBody[map[string]any](t, data); me["is_admin"] != false {
		t.Fatalf("non-admin /admin/me: %s", data)
	}
	_, data = s.get(t, "/api/v1/admin/me", admin)
	if me := decodeBody[map[string]any](t, data); me["is_admin"] != true {
		t.Fatalf("admin /admin/me: %s", data)
	}
}

func TestAdminUserLifecycle(t *testing.T) {
	s := newTestServer(t)
	admin := register(t, s, "Admin", "101", "password")

	resp, data := s.do(t, http.MethodPost, "/api/v1/admin/users", map[string]any{
		"name": "علی رضایی", "extension": "201", "password": "secret1",
	}, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	if resp, _ := s.do(t, http.MethodPost, "/api/v1/admin/users", map[string]any{
		"name": "Dup", "extension": "201", "password": "secret1",
	}, admin); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate: %d", resp.StatusCode)
	}
	if resp, _ := s.do(t, http.MethodPost, "/api/v1/admin/users", map[string]any{
		"name": "Bad", "extension": "20", "password": "secret1",
	}, admin); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid extension: %d", resp.StatusCode)
	}

	resp, data = s.do(t, http.MethodPatch, "/api/v1/admin/users/201", map[string]any{"name": "علی احمدی"}, admin)
	if resp.StatusCode != http.StatusOK || decodeBody[adminUser](t, data).Name != "علی احمدی" {
		t.Fatalf("rename: %d %s", resp.StatusCode, data)
	}

	// Password reset: old password stops working, new one works.
	if resp, data := s.do(t, http.MethodPost, "/api/v1/admin/users/201/password", map[string]any{"password": "newsecret"}, admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reset: %d %s", resp.StatusCode, data)
	}
	if resp, _ := s.post(t, "/api/v1/auth/login", map[string]any{"extension": "201", "password": "secret1"}, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password still valid: %d", resp.StatusCode)
	}
	resp, data = s.post(t, "/api/v1/auth/login", map[string]any{"extension": "201", "password": "newsecret"}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new password login: %d %s", resp.StatusCode, data)
	}
	userToken := decodeBody[tokenResponse](t, data).AccessToken

	if resp, _ := s.do(t, http.MethodDelete, "/api/v1/admin/users/101", nil, admin); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("self delete must be refused: %d", resp.StatusCode)
	}
	if resp, _ := s.do(t, http.MethodDelete, "/api/v1/admin/users/201", nil, admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ := s.get(t, "/api/v1/me", userToken); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deleted user's token must stop working: %d", resp.StatusCode)
	}
	if resp, _ := s.do(t, http.MethodDelete, "/api/v1/admin/users/201", nil, admin); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete twice: %d", resp.StatusCode)
	}
}

func TestAdminStatsAndCalls(t *testing.T) {
	s := newTestServer(t)
	admin := register(t, s, "Admin", "101", "password")
	register(t, s, "User", "102", "password")

	resp, data := s.get(t, "/api/v1/admin/stats", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats: %d %s", resp.StatusCode, data)
	}
	st := decodeBody[map[string]any](t, data)
	if st["users"] != float64(2) || st["calls_total"] != float64(0) {
		t.Fatalf("unexpected stats: %s", data)
	}

	resp, data = s.get(t, "/api/v1/admin/calls?limit=5", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("calls: %d %s", resp.StatusCode, data)
	}
	if calls := decodeBody[map[string]any](t, data)["calls"]; calls == nil {
		t.Fatalf("calls must be an array, got null: %s", data)
	}
}
