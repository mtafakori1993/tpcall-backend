package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tpcall-backend/internal/auth"
	"tpcall-backend/internal/config"
	"tpcall-backend/internal/database"
	"tpcall-backend/internal/history"
	"tpcall-backend/internal/httpapi"
	"tpcall-backend/internal/logging"
	"tpcall-backend/internal/signaling"
	"tpcall-backend/internal/users"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid_configuration", "error", err.Error())
		os.Exit(1)
	}
	logger := logging.Setup(cfg.LogLevel)

	ctx := context.Background()

	pool, err := database.Connect(ctx, cfg.DatabaseURL, logger)
	if err != nil {
		logger.Error("database_connect_failed", "error", err.Error())
		os.Exit(1)
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool, cfg.MigrationsDir, logger); err != nil {
		logger.Error("migrations_failed", "error", err.Error())
		os.Exit(1)
	}

	store := users.NewPostgresStore(pool)
	callHistory := history.NewPostgresStore(pool)
	authSvc := auth.NewService(store, []byte(cfg.JWTSecret), cfg.JWTExpiration)
	hub := signaling.NewHub(store, callHistory, logger, []byte(cfg.JWTSecret), cfg.RingTimeout)
	srv := httpapi.NewServer(cfg, logger, pool, store, callHistory, authSvc, hub)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr(),
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server_started",
			"event", "server_started",
			"listen", cfg.ListenAddr(),
			"advertised_url", cfg.PublicBaseURL,
		)
		errCh <- httpServer.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("shutdown_requested", "signal", sig.String())
	case serveErr := <-errCh:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("server_error", "error", serveErr.Error())
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Stop accepting new HTTP requests.
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http_shutdown_failed", "error", err.Error())
	}

	// Close WebSocket connections and let their cleanup finish.
	hub.Shutdown(shutdownCtx)

	// Close the database pool.
	pool.Close()

	logger.Info("shutdown_complete")
}
