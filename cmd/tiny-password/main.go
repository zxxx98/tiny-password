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
	"github.com/tiny-password/tiny-password/internal/platform/ephemeral"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/scheduler"
	"github.com/tiny-password/tiny-password/internal/settings"
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
	auditService := audit.NewService(audit.Options{})

	db, err := sqlite.Open(filepath.Join(dataDir, "tiny-password.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	// The upgrade guard snapshots non-empty databases before touching the
	// schema; the data-dir lock is already held (no parallel online writes).
	if presnapshot, err := sqlite.UpgradeWithHook(db, migrations.FS, dataDir, time.Now(), func(db *sql.DB, version int64) {
		// Migrations are already committed; audit persistence is best effort
		// and must never make a successful upgrade appear failed.
		_ = auditService.Record(context.Background(), db, audit.Event{
			Name:       audit.EventDatabaseMigration,
			ActorID:    audit.Anonymous,
			TargetType: audit.TargetSystem,
			TargetID:   fmt.Sprintf("schema-%d", version),
			Result:     audit.ResultSuccess,
		})
	}); err != nil {
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

	// Non-sensitive admin settings (T27). The R2 delivery resolver combines
	// them with the credential secret files on every run, so configuration
	// changes apply without a restart; credentials never enter the store.
	settingsService := settings.NewService(db.DB)
	backupPassphrase, err := backup.ReadOptionalSecretFile(
		envOr("TP_BACKUP_PASSPHRASE_FILE", "/run/secrets/backup_passphrase"),
	)
	if err != nil {
		return fmt.Errorf("backup passphrase secret: %w", err)
	}
	r2AccessPath := envOr("TP_R2_ACCESS_KEY_FILE", "/run/secrets/r2_access_key")
	r2SecretPath := envOr("TP_R2_SECRET_KEY_FILE", "/run/secrets/r2_secret_key")
	r2Resolver := backup.NewSettingsR2Resolver(db.DB, settingsService, r2AccessPath, r2SecretPath)

	// Vault item endpoints need the master key to seal payloads; an instance
	// without a key stays healthy but exposes no vault surface.
	var itemsDeps *httpapi.ItemsDeps
	var transferDeps *httpapi.TransferDeps
	var transferService *transfer.Service
	var previewCleanup httpapi.PreviewCleanup
	var backupRunner *backup.Runner
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
		// Personal import/export staging must live on an approved tmpfs mount
		// (D11). PrepareWorkDir validates both the default and the operator's
		// TP_TRANSFER_WORK_DIR, then creates one private child per process.
		workDir, cleanupWorkDir, err := transfer.PrepareWorkDir(os.Getenv("TP_TRANSFER_WORK_DIR"))
		if err != nil {
			return fmt.Errorf("transfer staging unavailable: %w", err)
		}
		defer cleanupWorkDir()
		transferService, err = transfer.NewService(vaultService, transfer.Options{
			WorkDir: workDir,
			HMACKey: masterKey.IdempotencyMACKey(),
			Audit:   auditService,
			DB:      db.DB,
		})
		if err != nil {
			return fmt.Errorf("transfer service: %w", err)
		}
		previewCleanup = transferService
		transferDeps = &httpapi.TransferDeps{Service: transferService, Session: authService}

		// Whole-instance backups (M5). The runner stages everything in a
		// restricted work dir and publishes verified archives only. The raw
		// master key copy passes through staging, so D11 applies: the work
		// dir is a private child of a verified tmpfs — the default is
		// discovered, a configured TP_BACKUP_WORK_DIR is refused unless it
		// really is tmpfs (never a persistent-volume fallback).
		backupWorkDir, cleanupBackupWorkDir, err := ephemeral.PrepareWorkDir(os.Getenv("TP_BACKUP_WORK_DIR"), "tiny-password-backup-")
		if err != nil {
			return fmt.Errorf("backup staging unavailable: %w", err)
		}
		defer cleanupBackupWorkDir()
		backupOptions := backup.Options{
			DB:                  db,
			WorkDir:             backupWorkDir,
			AppVersion:          version,
			MasterKeyRaw:        masterKeyRaw,
			ScheduledPassphrase: backupPassphrase,
			ScheduledLocalDir:   envOr("TP_BACKUP_DIR", filepath.Join(dataDir, "backups")),
			R2:                  r2Resolver,
			Audit:               auditService,
		}
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
	maintenanceDeps := backup.MaintenanceDeps{DB: db, Vault: vaultService, Idempotency: idempotencyService, R2Incoming: r2Resolver, Audit: auditService}
	for _, job := range backup.MaintenanceJobs(maintenanceDeps) {
		if err := sched.Register(job); err != nil {
			return fmt.Errorf("register %s: %w", job.Name, err)
		}
	}
	sched.Start(context.WithoutCancel(context.Background()))
	defer sched.Stop()

	// Admin backup/settings endpoints (T27). The manual passphrase and
	// delivery configuration resolve from secret files and the settings
	// store; secrets never appear in responses.
	var backupsDeps *httpapi.BackupsDeps
	if backupRunner != nil {
		backupsDeps = &httpapi.BackupsDeps{
			Runner:     backupRunner,
			DB:         db,
			LocalDir:   envOr("TP_BACKUP_DIR", filepath.Join(dataDir, "backups")),
			R2:         r2Resolver,
			Passphrase: backupPassphrase,
			Session:    authService,
			Cursor:     cursorCodec,
			Scheduler:  sched,
			Audit:      auditService,
		}
	}

	server := &http.Server{
		Addr: addr,
		Handler: httpapi.New(httpapi.Options{
			SPA:        webassets.SPAHandler(),
			Ready:      ready,
			Logger:     logger,
			Proxy:      proxy,
			Auth:       &httpapi.AuthDeps{Service: authService, CSRF: csrf, AllowInsecureCookies: config.AllowInsecureCookies(), Proxy: proxy, PreviewCleanup: previewCleanup},
			Audit:      &httpapi.AuditDeps{Service: auditService, DB: db, Cursor: cursorCodec, Session: authService},
			Users:      &httpapi.UsersDeps{Service: usersService, Session: authService, Cursor: cursorCodec, Idempotency: idempotencyService, PreviewCleanup: previewCleanup},
			Items:      itemsDeps,
			Generators: &httpapi.GeneratorsDeps{Session: authService},
			Transfer:   transferDeps,
			Backups:    backupsDeps,
			Settings: &httpapi.SettingsDeps{
				Settings:  settingsService,
				Session:   authService,
				Scheduler: sched,
				Version:   version,
				Ready:     func() (map[string]bool, bool) { return ready.Run(context.Background()) },
				R2CredentialsPresent: func() bool {
					return backup.R2CredentialsPresent(r2AccessPath, r2SecretPath)
				},
			},
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
	if transferService != nil {
		transferService.Start(ctx)
		defer transferService.Close()
	}

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

// A missing master key still permits health/readiness diagnostics, but cannot
// enable persisted request fingerprints with a replacement random secret.
func newIdempotencyService(db *sql.DB, key *crypto.MasterKey) (*idempotency.Service, error) {
	if key == nil {
		return nil, nil
	}
	return idempotency.NewService(db, idempotency.Options{MACKey: key.IdempotencyMACKey()})
}
