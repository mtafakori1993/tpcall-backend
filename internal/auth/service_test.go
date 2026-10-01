package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"tpcall-backend/internal/users"
)

func newTestService() *Service {
	return NewService(users.NewInMemoryStore(), []byte("test-secret"), time.Hour)
}

func TestRegisterCreatesUser(t *testing.T) {
	svc := newTestService()
	u, tokens, err := svc.Register(context.Background(), "محمد تفکری", "101", "password1")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.Extension != "101" || u.Name != "محمد تفکری" {
		t.Fatalf("unexpected user: %+v", u)
	}
	if u.PasswordHash == "" || u.PasswordHash == "password1" {
		t.Fatal("password must be hashed")
	}
	if tokens.AccessToken == "" || tokens.TokenType != "Bearer" {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}
}

func TestRegisterDuplicateExtension(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, _, err := svc.Register(ctx, "A", "101", "password1"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, _, err := svc.Register(ctx, "B", "101", "password2")
	if !errors.Is(err, users.ErrExtensionExists) {
		t.Fatalf("expected ErrExtensionExists, got %v", err)
	}
}

func TestLoginSuccess(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, _, err := svc.Register(ctx, "Mohammad", "101", "password1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	u, tokens, err := svc.Login(ctx, "101", "password1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if u.Extension != "101" || tokens.AccessToken == "" {
		t.Fatalf("unexpected login result: %+v %+v", u, tokens)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, _, err := svc.Register(ctx, "Mohammad", "101", "password1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, err := svc.Login(ctx, "101", "wrongpass"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLoginUnknownExtension(t *testing.T) {
	svc := newTestService()
	_, _, err := svc.Login(context.Background(), "404", "password1")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}
