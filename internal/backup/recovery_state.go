package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// Restore lifecycle stages persisted in the recovery state file. Every
// breakpoint restart selects either the complete old database or the
// complete new one: before "switched" the old database is untouched; at
// "switched" the candidate is fully built, verified, and atomically in
// place, so resuming only has to re-run the post-switch check.
const (
	StageSnapshot  = "snapshot"
	StageCandidate = "candidate"
	StageRekeyed   = "rekeyed"
	StageSwitched  = "switched"
)

// RecoveryState describes an interrupted restore. The file lives in the
// data directory and is removed when a restore completes or rolls back.
type RecoveryState struct {
	Stage           string `json:"stage"`
	BackupID        string `json:"backup_id"`
	ArchivePath     string `json:"archive_path"`
	PresnapshotPath string `json:"presnapshot_path,omitempty"`
	CandidatePath   string `json:"candidate_path,omitempty"`
	EmptyTarget     bool   `json:"empty_target"`
	StartedAt       string `json:"started_at"`
	UpdatedAt       string `json:"updated_at"`
}

// ErrRestoreCrash is returned by fault-injection hooks to abort without any
// cleanup or rollback — it emulates a process death between stages.
var ErrRestoreCrash = errors.New("restore: simulated crash")

func recoveryStatePath(dataDir string) string {
	return dataDir + "/restore-state.json"
}

// ReadRecoveryState exposes the persisted restore state for operators and
// tests; nil means no interrupted restore.
func ReadRecoveryState(dataDir string) (*RecoveryState, error) {
	raw, err := os.ReadFile(recoveryStatePath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read recovery state: %w", err)
	}
	var st RecoveryState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("parse recovery state: %w", err)
	}
	return &st, nil
}

func writeRecoveryState(dataDir string, st *RecoveryState) error {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// Write through a temporary file and rename: a torn direct write would
	// leave an unparseable state file and block breakpoint recovery.
	path := recoveryStatePath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func removeRecoveryState(dataDir string) {
	_ = os.Remove(recoveryStatePath(dataDir))
}
