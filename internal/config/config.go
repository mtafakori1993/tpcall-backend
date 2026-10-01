package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for the backend.
type Config struct {
	HTTPHost      string
	HTTPPort      string
	PublicBaseURL string
	DatabaseURL   string
	JWTSecret     string
	JWTExpiration time.Duration
	RingTimeout   time.Duration
	LogLevel      string
	CORSOrigins   []string
	ICEServers    []string
	MigrationsDir string
}

// Load reads configuration from environment variables.
// Mandatory values (DATABASE_URL, JWT_SECRET) cause startup failure when missing.
func Load() (*Config, error) {
	exp, err := durationEnv("JWT_EXPIRATION", 24*time.Hour)
	if err != nil {
		return nil, err
	}
	ring, err := durationEnv("RING_TIMEOUT", 30*time.Second)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		HTTPHost:      envStr("HTTP_HOST", "0.0.0.0"),
		HTTPPort:      envStr("HTTP_PORT", "9090"),
		PublicBaseURL: envStr("PUBLIC_BASE_URL", "http://192.168.1.200:9090"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		JWTSecret:     os.Getenv("JWT_SECRET"),
		JWTExpiration: exp,
		RingTimeout:   ring,
		LogLevel:      envStr("LOG_LEVEL", "info"),
		CORSOrigins:   csvEnv("CORS_ORIGINS", "*"),
		ICEServers:    csvEnv("ICE_SERVERS", ""),
		MigrationsDir: envStr("MIGRATIONS_DIR", "./migrations"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("mandatory configuration missing: DATABASE_URL")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("mandatory configuration missing: JWT_SECRET")
	}
	if _, err := strconv.Atoi(cfg.HTTPPort); err != nil {
		return nil, fmt.Errorf("HTTP_PORT must be a numeric port, got %q", cfg.HTTPPort)
	}
	if cfg.JWTExpiration <= 0 {
		return nil, fmt.Errorf("JWT_EXPIRATION must be positive, got %s", cfg.JWTExpiration)
	}
	return cfg, nil
}

// ListenAddr returns the host:port the HTTP server binds to.
func (c *Config) ListenAddr() string {
	return net.JoinHostPort(c.HTTPHost, c.HTTPPort)
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if strings.TrimSpace(v) == "" {
		return def, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return d, nil
}

func csvEnv(key, def string) []string {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		if def == "" {
			return nil
		}
		raw = def
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
