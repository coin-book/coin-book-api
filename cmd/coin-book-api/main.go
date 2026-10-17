package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rotisserie/eris"

	"coin-book-api/db/seed"
	"coin-book-api/internal/app"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/config"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/migrate"
)

var (
	buildVersion string
	buildCommit  string
)

const shutdownBudget = 20 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetDefault(newLogger(cfg.Log))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	connStr := cfg.Postgres.ConnectionString()
	if err := db.WaitReady(ctx, connStr, time.Minute); err != nil {
		return err
	}
	if err := migrate.Up(connStr); err != nil {
		return err
	}
	slog.Info("migrations applied")
	if err := db.PrepareAppRole(ctx, connStr); err != nil {
		return err
	}
	pool, err := db.Open(ctx, connStr)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.Server.SeedDemo {
		if err := seed.Demo(ctx, pool, cfg.Storage.ImportsDir); err != nil {
			return eris.Wrap(err, "apply demo seed")
		}
		slog.Warn("demo data applied — local development only", "email", seed.DemoEmail, "password", seed.DemoPassword)
	}

	handler, _ := app.New(cfg, pool, clock.System{}, app.Build{Version: buildVersion, Commit: buildCommit})
	server := &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", server.Addr, "version", buildVersion)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return eris.Wrap(err, "serve")
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	return eris.Wrap(server.Shutdown(shutdownCtx), "shut down")
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(cfg.Level))
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(cfg.Format, "text") {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
