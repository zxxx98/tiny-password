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
	"strconv"
	"syscall"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/idempotency"
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

	bootService, err := bootstrap.NewService(db, masterKey, logger)
	if err != nil {
		return fmt.Errorf("bootstrap service: %w", err)
	}

	setupRateLimit := 10
	if v := os.Getenv("TP_SETUP_RATE_LIMIT_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			setupRateLimit = n
		}
	}

	csrf := httpapi.NewPreAuthCSRF(config.AllowInsecureCookies())

	// Trusted proxy networks come from explicit configuration only; with no
	// configuration every forwarded header is ignored.
	proxyCIDRs, err := config.TrustedProxyCIDRs()
	if err != nil {
		return fmt.Errorf("trusted proxy configuration: %w", err)
	}
	proxy := httpapi.ProxyConfig{Trusted: proxyCIDRs}

	// Per-process HMAC secrets for idempotency fingerprints and pagination
	// cursors. Outstanding cursors do not survive restarts (transient state).
	idemKey, err := httpapi.NewCursorMACKey()
	if err != nil {
		return fmt.Errorf("idempotency key: %w", err)
	}
	idempotencyService, err := idempotency.NewService(db.DB, idempotency.Options{MACKey: idemKey})
	if err != nil {
		return fmt.Errorf("idempotency service: %w", err)
	}
	cursorKey, err := httpapi.NewCursorMACKey()
	if err != nil {
		return fmt.Errorf("cursor key: %w", err)
	}
	cursorCodec, err := httpapi.NewCursorCodec(cursorKey, 0)
	if err != nil {
		return fmt.Errorf("cursor codec: %w", err)
	}
	auditService := audit.NewService(audit.Options{})

	authService, err := auth.NewService(db.DB, auth.Options{Audit: auditService, Logger: logger})
	if err != nil {
		return fmt.Errorf("auth service: %w", err)
	}

	server := &http.Server{
		Addr: addr,
		Handler: httpapi.New(httpapi.Options{
			SPA:    webassets.SPAHandler(),
			Ready:  ready,
			Logger: logger,
			Proxy:  proxy,
			Auth:   &httpapi.AuthDeps{Service: authService, CSRF: csrf, AllowInsecureCookies: config.AllowInsecureCookies(), Proxy: proxy},
			Audit:  &httpapi.AuditDeps{Service: auditService, DB: db, Cursor: cursorCodec, Session: authService},
			Setup: &httpapi.SetupDeps{
				Service:     bootService,
				CSRF:        csrf,
				Logger:      logger,
				RateLimit:   setupRateLimit,
				Proxy:       proxy,
				Idempotency: idempotencyService,
			},
		}),
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
