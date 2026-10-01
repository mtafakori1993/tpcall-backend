package users

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// InMemoryStore is a thread-safe Store used by tests.
type InMemoryStore struct {
	mu    sync.RWMutex
	byExt map[string]User
	byID  map[uuid.UUID]User
}

// NewInMemoryStore returns an empty in-memory user store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		byExt: make(map[string]User),
		byID:  make(map[uuid.UUID]User),
	}
}

func (s *InMemoryStore) CreateUser(ctx context.Context, name, extension, passwordHash string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byExt[extension]; ok {
		return User{}, ErrExtensionExists
	}
	u := User{
		ID:           uuid.New(),
		Name:         name,
		Extension:    extension,
		PasswordHash: passwordHash,
	}
	s.byExt[u.Extension] = u
	s.byID[u.ID] = u
	return u, nil
}

func (s *InMemoryStore) GetUserByExtension(ctx context.Context, extension string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byExt[extension]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (s *InMemoryStore) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (s *InMemoryStore) ListUsers(ctx context.Context) ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.byExt))
	for _, u := range s.byExt {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Extension < out[j].Extension })
	return out, nil
}

func (s *InMemoryStore) UpdateLastLogin(ctx context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return ErrUserNotFound
	}
	now := time.Now()
	u.LastLoginAt = &now
	s.byID[id] = u
	s.byExt[u.Extension] = u
	return nil
}

func (s *InMemoryStore) UpdateName(ctx context.Context, id uuid.UUID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return ErrUserNotFound
	}
	u.Name = name
	s.byID[id] = u
	s.byExt[u.Extension] = u
	return nil
}

func (s *InMemoryStore) UpdatePasswordHash(ctx context.Context, id uuid.UUID, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return ErrUserNotFound
	}
	u.PasswordHash = passwordHash
	s.byID[id] = u
	s.byExt[u.Extension] = u
	return nil
}

func (s *InMemoryStore) DeleteUser(ctx context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return ErrUserNotFound
	}
	delete(s.byID, id)
	delete(s.byExt, u.Extension)
	return nil
}
