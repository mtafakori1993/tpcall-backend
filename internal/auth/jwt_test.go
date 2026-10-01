package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSignAndParseToken(t *testing.T) {
	secret := []byte("test-secret-1234567890")
	id := uuid.New()
	token, err := SignToken(secret, id, "Mohammad", "101", time.Hour)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	claims, err := ParseToken(secret, token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.Subject != id.String() {
		t.Fatalf("subject = %q, want %q", claims.Subject, id.String())
	}
	if claims.Extension != "101" {
		t.Fatalf("extension = %q, want 101", claims.Extension)
	}
	if claims.Name != "Mohammad" {
		t.Fatalf("name = %q, want Mohammad", claims.Name)
	}
}

func TestParseTokenRejectsWrongSecret(t *testing.T) {
	token, err := SignToken([]byte("secret-a"), uuid.New(), "A", "101", time.Hour)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if _, err := ParseToken([]byte("secret-b"), token); err == nil {
		t.Fatal("expected error with wrong secret")
	}
}

func TestParseTokenRejectsExpired(t *testing.T) {
	token, err := SignToken([]byte("secret"), uuid.New(), "A", "101", -time.Minute)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if _, err := ParseToken([]byte("secret"), token); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestParseTokenRejectsGarbage(t *testing.T) {
	if _, err := ParseToken([]byte("secret"), "not.a.jwt"); err == nil {
		t.Fatal("expected error for garbage token")
	}
}
