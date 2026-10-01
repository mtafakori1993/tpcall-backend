package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("s3cr3t-pass")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" || !strings.HasPrefix(hash, "$argon2id$v=") {
		t.Fatalf("unexpected hash format: %q", hash)
	}

	ok, err := VerifyPassword("s3cr3t-pass", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("expected password to verify")
	}

	ok, err = VerifyPassword("wrong-password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword wrong: %v", err)
	}
	if ok {
		t.Fatal("expected wrong password to fail verification")
	}
}

func TestHashIsSalted(t *testing.T) {
	h1, _ := HashPassword("same-password")
	h2, _ := HashPassword("same-password")
	if h1 == h2 {
		t.Fatal("two hashes of the same password must differ (salt)")
	}
}

func TestVerifyPasswordRejectsGarbage(t *testing.T) {
	for _, encoded := range []string{"", "plaintext", "$argon2id$v=19$m=1,t=1,p=1$AAAA$AAAA", "$bcrypt$abc"} {
		ok, err := VerifyPassword("anything", encoded)
		if err == nil && ok {
			t.Fatalf("expected invalid encoded hash %q to fail", encoded)
		}
	}
}
