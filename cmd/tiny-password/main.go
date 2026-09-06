// Command tiny-password runs the Tiny Password single-binary HTTP service
// (or, in later milestones, offline restore subcommands). main only wires
// dependencies; behavior lives in internal packages.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/backup"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/config"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/objectstore"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/scheduler"
	"github.com/tiny-password/tiny-password/internal/transfer"
	"github.com/tiny-password/tiny-password/internal/users"
	"github.com/tiny-password/tiny-password/internal/vault"
	"github.com/tiny-password/tiny-password/internal/webassets"
	"github.com/tiny-password/tiny-password/migrations"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
	// Offline restore runs instead of the HTTP service; the data-directory
	// lock keeps the two mutually exclusive (T25).
	if len(os.Args) > 1 && os.Args[1] == "restore" {
		os.Exit(runRestoreCommand(os.Args[2:], logger))
	}
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
	// The service holds the data-directory lock for its whole lifetime so
	// an offline restore can never race a running instance (T25).
	dataDirLock, err := backup.AcquireDataDirLock(dataDir)
	if err != nil {
		return fmt.Errorf("acquire data dir lock: %w", err)
	}
	defer dataDirLock.Release()

	db, err := sqlite.Open(filepath.Join(dataDir, "tiny-password.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	// The upgrade guard snapshots non-empty databases before touching the
	// schema; the data-dir lock is already held (no parallel online writes).
	if presnapshot, err := sqlite.Upgrade(db, migrations.FS, dataDir, time.Now()); err != nil {
		if presnapshot != "" {
			return fmt.Errorf("apply migrations: %w (pre-upgrade snapshot: %s)", err, filepath.Base(presnapshot))
		}
		return fmt.Errorf("apply migrations: %w", err)
	}

	// Master key is read from the mounted secret file only (D03). A missing
	// or invalid key keeps the process alive (healthz ok) but not ready.
	keyFile := config.MasterKeyFile()
	var masterKeyRaw []byte
	masterKey, masterKeyErr := func() (*crypto.MasterKey, error) {
		raw, err := config.ReadMasterKeyFile(keyFile)
		if err != nil {
			return nil, err
		}
		masterKeyRaw = raw
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

	idempotencyService, err := newIdempotencyService(db.DB, masterKey)
	if err != nil {
		return fmt.Errorf("idempotency service: %w", err)
	}
	// Pagination cursors are transient and intentionally expire on restart.
	cursorKey, err := httpapi.NewCursorMACKey()
	if err != nil {
		return fmt.Errorf("cursor key: %w", err)
	}
	cursorCodec, err := httpapi.NewCursorCodec(cursorKey, 0)
	if err != nil {
		return fmt.Errorf("cursor codec: %w", err)
	}
	auditService := audit.NewService(audit.Options{})

	// Login rate limits follow the production defaults; test harnesses may
	// raise the username window explicitly (mirrors TP_SETUP_RATE_LIMIT_PER_MIN).
	authLimits := auth.Limits{}
	if v := os.Getenv("TP_AUTH_LOGIN_LIMIT_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			authLimits = auth.Limits{Username: n, Source: n * 5, Global: n * 50}
		}
	}
	authService, err := auth.NewService(db.DB, auth.Options{Audit: auditService, Logger: logger, Limits: authLimits})
	if err != nil {
		return fmt.Errorf("auth service: %w", err)
	}
	usersService := users.NewService(db.DB, users.Options{Audit: auditService})

	// Vault item endpoints need the master key to seal payloads; an instance
	// without a key stays healthy but exposes no vault surface.
	var itemsDeps *httpapi.ItemsDeps
	var transferDeps *httpapi.TransferDeps
	var backupRunner *backup.Runner
	var scheduledR2 *backup.R2Delivery
	var vaultService *vault.Service
	if masterKey != nil {
		vaultService, err = vault.NewService(db.DB, masterKey, vault.Options{Audit: auditService})
		if err != nil {
			return fmt.Errorf("vault service: %w", err)
		}
		itemsDeps = &httpapi.ItemsDeps{
			Service:     vaultService,
			Session:     authService,
			Cursor:      cursorCodec,
			Idempotency: idempotencyService,
		}
		// Personal import/export staging lives under the data dir with 0700
		// permissions (D11: a tmpfs mount in deployment).
		transferService, err := transfer.NewService(vaultService, transfer.Options{
			WorkDir: envOr("TP_TRANSFER_WORK_DIR", filepath.Join(dataDir, "transfer-tmp")),
			HMACKey: masterKey.IdempotencyMACKey(),
			Audit:   auditService,
			DB:      db.DB,
		})
		if err != nil {
			return fmt.Errorf("transfer service: %w", err)
		}
		transferDeps = &httpapi.TransferDeps{Service: transferService, Session: authService}

		// Whole-instance backups (M5). The runner stages everything in a
		// restricted work dir and publishes verified archives only.
		backupOptions := backup.Options{
			DB:                  db,
			WorkDir:             envOr("TP_BACKUP_WORK_DIR", filepath.Join(dataDir, "backup-tmp")),
			AppVersion:          version,
			MasterKeyRaw:        masterKeyRaw,
			ScheduledPassphrase: readOptionalSecret("TP_BACKUP_PASSPHRASE_FILE", "/run/secrets/backup_passphrase"),
			ScheduledLocalDir:   envOr("TP_BACKUP_DIR", filepath.Join(dataDir, "backups")),
		}
		scheduledR2 = r2DeliveryFromEnv(logger)
		backupOptions.ScheduledR2 = scheduledR2
		backupRunner, err = backup.NewRunner(backupOptions)
		if err != nil {
			return fmt.Errorf("backup runner: %w", err)
		}
		if marked, err := backup.MarkInterruptedRuns(db.DB, time.Now()); err != nil {
			logger.Warn("cannot mark interrupted backup runs", "error", err.Error())
		} else if marked > 0 {
			logger.Info("marked interrupted backup runs", "count", marked)
		}
	}

	// The scheduler owns daily backup jobs and bounded maintenance sweeps.
	sched, err := scheduler.New(scheduler.Options{DB: db.DB, Logger: logger})
	if err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}
	if backupRunner != nil {
		jobs, err := backupRunner.ScheduledJobs()
		if err != nil {
			return fmt.Errorf("scheduled backup jobs: %w", err)
		}
		for _, job := range jobs {
			if err := sched.Register(job); err != nil {
				return fmt.Errorf("register %s: %w", job.Name, err)
			}
		}
	}
	maintenanceDeps := backup.MaintenanceDeps{DB: db, Vault: vaultService, Idempotency: idempotencyService, R2Incoming: scheduledR2}
	for _, job := range backup.MaintenanceJobs(maintenanceDeps) {
		if err := sched.Register(job); err != nil {
			return fmt.Errorf("register %s: %w", job.Name, err)
		}
	}
	sched.Start(context.WithoutCancel(context.Background()))
	defer sched.Stop()

	server := &http.Server{
		Addr: addr,
		Handler: httpapi.New(httpapi.Options{
			SPA:        webassets.SPAHandler(),
			Ready:      ready,
			Logger:     logger,
			Proxy:      proxy,
			Auth:       &httpapi.AuthDeps{Service: authService, CSRF: csrf, AllowInsecureCookies: config.AllowInsecureCookies(), Proxy: proxy},
			Audit:      &httpapi.AuditDeps{Service: auditService, DB: db, Cursor: cursorCodec, Session: authService},
			Users:      &httpapi.UsersDeps{Service: usersService, Session: authService, Cursor: cursorCodec, Idempotency: idempotencyService},
			Items:      itemsDeps,
			Generators: &httpapi.GeneratorsDeps{Session: authService},
			Transfer:   transferDeps,
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

// readOptionalSecret reads a secret file that may legitimately be absent
// (e.g. scheduled backups disabled). Present-but-unreadable is fatal: the
// operator must fix the mount rather than run with partial credentials.
func readOptionalSecret(envKey, defaultPath string) string {
	path := envOr(envKey, defaultPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// r2DeliveryFromEnv assembles the R2 delivery from operator configuration.
// All-or-nothing: a partial configuration logs a warning and disables the
// target instead of guessing.
func r2DeliveryFromEnv(logger *slog.Logger) *backup.R2Delivery {
	endpoint := os.Getenv("TP_R2_ENDPOINT")
	bucket := os.Getenv("TP_R2_BUCKET")
	prefix := envOr("TP_R2_PREFIX", "tiny-password")
	access := readOptionalSecret("TP_R2_ACCESS_KEY_FILE", "/run/secrets/r2_access_key")
	secret := readOptionalSecret("TP_R2_SECRET_KEY_FILE", "/run/secrets/r2_secret_key")
	if endpoint == "" && bucket == "" && access == "" && secret == "" {
		return nil
	}
	if endpoint == "" || bucket == "" || access == "" || secret == "" {
		logger.Warn("incomplete R2 configuration; R2 backup target disabled")
		return nil
	}
	client, err := objectstore.NewClient(objectstore.Config{
		Endpoint:        endpoint,
		Region:          "auto",
		Bucket:          bucket,
		AccessKeyID:     access,
		SecretAccessKey: secret,
	})
	if err != nil {
		logger.Warn("invalid R2 configuration; R2 backup target disabled")
		return nil
	}
	return &backup.R2Delivery{Client: client, Prefix: prefix}
}

// A missing master key still permits health/readiness diagnostics, but cannot
// enable persisted request fingerprints with a replacement random secret.
func newIdempotencyService(db *sql.DB, key *crypto.MasterKey) (*idempotency.Service, error) {
	if key == nil {
		return nil, nil
	}
	return idempotency.NewService(db, idempotency.Options{MACKey: key.IdempotencyMACKey()})
}
