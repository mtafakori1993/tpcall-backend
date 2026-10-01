package users

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrExtensionExists is returned when creating a user with an existing extension.
	ErrExtensionExists = errors.New("extension already exists")
	// ErrUserNotFound is returned when a user cannot be found.
	ErrUserNotFound = errors.New("user not found")
)

// User is the persisted representation of an employee.
type User struct {
	ID           uuid.UUID
	Name         string
	Extension    string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

var extensionRe = regexp.MustCompile(`^[1-9][0-9]{2}$`)

// ValidExtension reports whether ext is exactly three ASCII digits between 100 and 999.
func ValidExtension(ext string) bool {
	return extensionRe.MatchString(ext)
}

// Store is the persistence abstraction for users.
type Store interface {
	CreateUser(ctx context.Context, name, extension, passwordHash string) (User, error)
	GetUserByExtension(ctx context.Context, extension string) (User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	UpdateLastLogin(ctx context.Context, id uuid.UUID) error
	// UpdateName changes the display name of a user.
	UpdateName(ctx context.Context, id uuid.UUID, name string) error
	// UpdatePasswordHash replaces the stored password hash of a user.
	UpdatePasswordHash(ctx context.Context, id uuid.UUID, passwordHash string) error
	// DeleteUser removes a user together with the call history they took part in.
	DeleteUser(ctx context.Context, id uuid.UUID) error
}

const errUniqueViolation = "23505"

// PostgresStore implements Store against PostgreSQL using parameterized SQL.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore returns a Store backed by the given connection pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) CreateUser(ctx context.Context, name, extension, passwordHash string) (User, error) {
	u := User{ID: uuid.New(), Name: name, Extension: extension, PasswordHash: passwordHash}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (id, name, extension, password_hash)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, name, extension, password_hash, created_at, updated_at, last_login_at`,
		u.ID, u.Name, u.Extension, u.PasswordHash,
	).Scan(&u.ID, &u.Name, &u.Extension, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == errUniqueViolation {
			return User{}, ErrExtensionExists
		}
		return User{}, err
	}
	return u, nil
}

func (s *PostgresStore) GetUserByExtension(ctx context.Context, extension string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, extension, password_hash, created_at, updated_at, last_login_at
		 FROM users WHERE extension = $1`, extension,
	).Scan(&u.ID, &u.Name, &u.Extension, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *PostgresStore) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, extension, password_hash, created_at, updated_at, last_login_at
		 FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Name, &u.Extension, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *PostgresStore) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, extension, password_hash, created_at, updated_at, last_login_at
		 FROM users ORDER BY extension`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Extension, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateLastLogin(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET last_login_at = NOW(), updated_at = NOW() WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) UpdateName(ctx context.Context, id uuid.UUID, name string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET name = $2, updated_at = NOW() WHERE id = $1`, id, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *PostgresStore) UpdatePasswordHash(ctx context.Context, id uuid.UUID, passwordHash string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = NOW() WHERE id = $1`, id, passwordHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// DeleteUser removes the user and every call_history row they were part of
// (the foreign keys have no cascade) in one transaction.
func (s *PostgresStore) DeleteUser(ctx context.Context, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	if _, err := tx.Exec(ctx,
		`DELETE FROM call_history WHERE caller_user_id = $1 OR callee_user_id = $1`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return tx.Commit(ctx)
}
