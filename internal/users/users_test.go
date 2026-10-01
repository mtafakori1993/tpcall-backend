package users

import (
	"context"
	"testing"
)

func TestValidExtension(t *testing.T) {
	valid := []string{"100", "101", "203", "999"}
	for _, ext := range valid {
		if !ValidExtension(ext) {
			t.Errorf("expected %q to be valid", ext)
		}
	}
	invalid := []string{"", "001", "020", "099", "abc", "10", "1000", "12a", "1 1", "-12", "10.5"}
	for _, ext := range invalid {
		if ValidExtension(ext) {
			t.Errorf("expected %q to be invalid", ext)
		}
	}
}

func TestInMemoryStore(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()

	u1, err := s.CreateUser(ctx, "Mohammad", "101", "hash1")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := s.CreateUser(ctx, "Other", "101", "hash2"); !IsErrExtensionExists(err) {
		t.Fatalf("expected ErrExtensionExists, got %v", err)
	}

	got, err := s.GetUserByExtension(ctx, "101")
	if err != nil {
		t.Fatalf("GetUserByExtension: %v", err)
	}
	if got.ID != u1.ID || got.PasswordHash != "hash1" {
		t.Fatalf("unexpected user: %+v", got)
	}

	if _, err := s.GetUserByExtension(ctx, "999"); !IsErrUserNotFound(err) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}

	byID, err := s.GetUserByID(ctx, u1.ID)
	if err != nil || byID.Extension != "101" {
		t.Fatalf("GetUserByID: %v %+v", err, byID)
	}

	if err := s.UpdateLastLogin(ctx, u1.ID); err != nil {
		t.Fatalf("UpdateLastLogin: %v", err)
	}
}

func TestInMemoryStoreListSorted(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	for _, ext := range []string{"305", "101", "203"} {
		if _, err := s.CreateUser(ctx, "U "+ext, ext, "hash"); err != nil {
			t.Fatalf("CreateUser %s: %v", ext, err)
		}
	}
	all, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	want := []string{"101", "203", "305"}
	for i, w := range want {
		if all[i].Extension != w {
			t.Fatalf("ListUsers[%d] = %s, want %s", i, all[i].Extension, w)
		}
	}
}

func IsErrExtensionExists(err error) bool { return err == ErrExtensionExists }
func IsErrUserNotFound(err error) bool    { return err == ErrUserNotFound }
