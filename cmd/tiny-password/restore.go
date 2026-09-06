package main

// The offline restore subcommand (design §11.5, T25):
//
//	tiny-password restore /restore/backup.7z
//
// It must run with the normal service stopped; the data-directory lock
// makes concurrent operation impossible. Reports contain only the failure
// stage, a stable code, and a request-style correlation id — never secrets
// or filesystem error text.

import (
	"context"
	"log/slog"

	"github.com/tiny-password/tiny-password/internal/backup"
	"github.com/tiny-password/tiny-password/internal/platform/config"
	"github.com/tiny-password/tiny-password/migrations"
)

const restoreCorrelationID = "restore-command"

func runRestoreCommand(args []string, logger *slog.Logger) int {
	if len(args) != 1 {
		logger.Error("usage: tiny-password restore <archive.7z>")
		return 2
	}
	dataDir := envOr("TP_DATA_DIR", "/data")
	passphrase := readOptionalSecret("TP_BACKUP_PASSPHRASE_FILE", "/run/secrets/backup_passphrase")

	targetKeyRaw, err := config.ReadMasterKeyFile(config.MasterKeyFile())
	if err != nil {
		// The mounted key is the restore target's key; a missing or
		// malformed one is a hard stop (D03: never generate replacements).
		logger.Error("restore failed", "stage", "start", "code", backup.RestoreCodeTargetKey, "request_id", restoreCorrelationID)
		return 1
	}

	result, err := backup.Restore(context.Background(), backup.RestoreOptions{
		DataDir:      dataDir,
		WorkDir:      envOr("TP_RESTORE_WORK_DIR", dataDir+"/restore-tmp"),
		ArchivePath:  args[0],
		Passphrase:   passphrase,
		TargetKeyRaw: targetKeyRaw,
		Migrations:   migrations.FS,
		AppVersion:   version,
		Logger:       logger,
	})
	if err != nil {
		if restoreErr, ok := err.(*backup.RestoreError); ok {
			logger.Error("restore failed", "stage", restoreErr.Stage, "code", restoreErr.Code, "request_id", restoreCorrelationID)
			return 1
		}
		logger.Error("restore failed", "stage", "unknown", "code", backup.RestoreCodeInternal, "request_id", restoreCorrelationID)
		return 1
	}
	logger.Info("restore complete",
		"backup_id", result.BackupID,
		"items", result.Items,
		"versions", result.Versions,
		"auth_cleared", result.AuthCleared,
		"presnapshot", result.PresnapshotPath != "",
		"resumed", result.Resumed,
		"request_id", restoreCorrelationID,
	)
	return 0
}
