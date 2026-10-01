-- Initial schema for tpcall-backend.

CREATE TABLE users (
    id            UUID PRIMARY KEY,
    name          VARCHAR(100) NOT NULL,
    extension     VARCHAR(3)   NOT NULL UNIQUE,
    password_hash TEXT         NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ,

    -- Extensions must be exactly three digits in the range 100..999.
    CONSTRAINT chk_users_extension CHECK (extension ~ '^[1-9][0-9]{2}$')
);
