package auth

import (
	"context"
	"errors"
	"time"

	"tpcall-backend/internal/users"
)

var (
	// ErrInvalidCredentials is returned when extension/password do not match.
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// Service handles registration, login and token issuance.
type Service struct {
	users      users.Store
	jwtSecret  []byte
	expiration time.Duration
}

// TokenPair is the token payload returned to clients.
type TokenPair struct {
	AccessToken string
	TokenType   string
}

// NewService creates an auth service.
func NewService(store users.Store, jwtSecret []byte, expiration time.Duration) *Service {
	return &Service{users: store, jwtSecret: jwtSecret, expiration: expiration}
}

// Register creates a user and returns it together with fresh tokens.
func (s *Service) Register(ctx context.Context, name, extension, password string) (users.User, TokenPair, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return users.User{}, TokenPair{}, err
	}
	u, err := s.users.CreateUser(ctx, name, extension, hash)
	if err != nil {
		return users.User{}, TokenPair{}, err
	}
	_ = s.users.UpdateLastLogin(ctx, u.ID)
	token, err := s.TokenFor(u)
	if err != nil {
		return users.User{}, TokenPair{}, err
	}
	return u, TokenPair{AccessToken: token, TokenType: "Bearer"}, nil
}

// Login verifies extension + password and returns the user with fresh tokens.
func (s *Service) Login(ctx context.Context, extension, password string) (users.User, TokenPair, error) {
	u, err := s.users.GetUserByExtension(ctx, extension)
	if err != nil {
		return users.User{}, TokenPair{}, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(password, u.PasswordHash)
	if err != nil || !ok {
		return users.User{}, TokenPair{}, ErrInvalidCredentials
	}
	_ = s.users.UpdateLastLogin(ctx, u.ID)
	token, err := s.TokenFor(u)
	if err != nil {
		return users.User{}, TokenPair{}, err
	}
	return u, TokenPair{AccessToken: token, TokenType: "Bearer"}, nil
}

// TokenFor issues a JWT for the given user.
func (s *Service) TokenFor(u users.User) (string, error) {
	return SignToken(s.jwtSecret, u.ID, u.Name, u.Extension, s.expiration)
}
