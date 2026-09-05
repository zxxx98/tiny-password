// Command tiny-password runs the Tiny Password single-binary HTTP service
// (or, in later milestones, offline restore subcommands). main only wires
// dependencies; behavior lives in internal packages.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/platform/config"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/webassets"
	"github.com/tiny-password/tiny-password/migrations"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("startup failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := envOr("TP_ADDR", ":8080")
	dataDir := envOr("TP_DATA_DIR", "/data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}

	db, err := sqlite.Open(filepath.Join(dataDir, "tiny-password.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	// Master key is read from the mounted secret file only (D03). A missing
	// or invalid key keeps the process alive (healthz ok) but not ready.
	keyFile := config.MasterKeyFile()
	masterKey, masterKeyErr := func() (*crypto.MasterKey, error) {
		raw, err := config.ReadMasterKeyFile(keyFile)
		if err != nil {
			return nil, err
		}
		return crypto.NewMasterKey(raw)
	}()
	if masterKeyErr != nil {
		logger.Warn("master key unavailable; readiness will fail", "file", keyFile, "error", masterKeyErr.Error())
	}

	ready := &httpapi.ReadyChecker{DB: db, MasterKeyCheck: bootstrap.MasterKeyCheck(db.DB, masterKey, masterKeyErr)}
	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.New(httpapi.Options{SPA: webassets.SPAHandler(), Ready: ready}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownErr := make(chan error, 1)
	go func() {
		<-ctx.Done()
		logger.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		shutdownErr <- server.Shutdown(shutdownCtx)
	}()

	logger.Info("tiny-password listening", "addr", addr, "data_dir", filepath.Clean(dataDir), "version", version)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if err := <-shutdownErr; err != nil {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
