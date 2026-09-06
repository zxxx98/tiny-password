package transfer

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// PreviewTTL bounds how long a preview stays confirmable (decision: short
// window bound to the caller).
const PreviewTTL = 10 * time.Minute

const previewSweepInterval = time.Minute

// A preview directory is first created and then populated with metadata and
// payload. A short grace lets that sequence finish without retaining a
// partially-created directory forever after a crash.
const previewCreationGrace = 30 * time.Second

// Consuming directories are not swept while a confirmation may still be
// running. A process that crashes mid-confirm is reclaimed after a generous
// lease, while normal confirms finish well inside this bound.
const consumingLease = time.Hour

// Service orchestrates personal export/import on top of the vault service
// and the pinned archive adapter.
type Service struct {
	vault       *vault.Service
	audit       *audit.Service
	db          *sql.DB
	workDir     string
	hmacKey     []byte
	now         func() time.Time
	lifecycleMu sync.Mutex
	started     bool
	stop        chan struct{}
	done        chan struct{}
	stopOnce    sync.Once
}

// Options configures the transfer service. WorkDir should be a restricted
// tmpfs mount in deployment (decision D11); HMACKey signs preview tokens
// (≥32 bytes).
type Options struct {
	WorkDir string
	HMACKey []byte
	Audit   *audit.Service
	Now     func() time.Time
	// DB carries audit writes (the same pool the vault uses).
	DB *sql.DB
}

// Start begins periodic preview cleanup. It is intentionally explicit so
// tests and embedders can control the lifecycle; the command wires it to its
// process context. Cleanup runs immediately and then at a bounded interval,
// so expired plaintext is removed even when no new request arrives.
func (s *Service) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycleMu.Lock()
	if s.started {
		s.lifecycleMu.Unlock()
		return
	}
	s.started = true
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	stop, done := s.stop, s.done
	s.lifecycleMu.Unlock()
	go func() {
		defer close(done)
		s.sweepExpiredPreviews()
		ticker := time.NewTicker(previewSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.sweepExpiredPreviews()
			case <-ctx.Done():
				return
			case <-stop:
				return
			}
		}
	}()
}

// Close stops periodic cleanup and waits for the worker to exit. Calling it
// on a service that was never started is safe.
func (s *Service) Close() {
	s.lifecycleMu.Lock()
	if !s.started {
		s.lifecycleMu.Unlock()
		s.removeStagedPreviews()
		return
	}
	stop, done := s.stop, s.done
	s.lifecycleMu.Unlock()
	s.stopOnce.Do(func() { close(stop) })
	<-done
	s.removeStagedPreviews()
}

// NewService validates the configuration.
func NewService(v *vault.Service, options Options) (*Service, error) {
	if v == nil {
		return nil, fmt.Errorf("transfer: vault service is required")
	}
	if len(options.HMACKey) < 32 {
		return nil, fmt.Errorf("transfer: HMAC key must be at least 32 bytes")
	}
	if options.WorkDir == "" {
		return nil, fmt.Errorf("transfer: work dir is required")
	}
	if err := os.MkdirAll(options.WorkDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(options.WorkDir, 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Stat(options.WorkDir); err != nil || !info.IsDir() {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("transfer: work dir is not a directory")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Audit == nil {
		options.Audit = audit.NewService(audit.Options{})
	}
	if options.DB == nil {
		return nil, fmt.Errorf("transfer: db is required")
	}
	return &Service{
		vault:   v,
		audit:   options.Audit,
		db:      options.DB,
		workDir: options.WorkDir,
		hmacKey: options.HMACKey,
		now:     options.Now,
	}, nil
}

// Export builds the caller's personal archive and returns its path plus a
// cleanup function the HTTP handler MUST call after streaming.
func (s *Service) Export(ctx context.Context, actor *auth.Principal, input ExportInput) (string, func(), error) {
	if err := ValidateExportInput(input); err != nil {
		return "", nil, err
	}
	items, err := s.vault.ExportAll(ctx, actor)
	if err != nil {
		return "", nil, err
	}
	work, err := archive.TempFileUnder(s.workDir) // 0700 dir, random name
	if err != nil {
		return "", nil, err
	}
	workPath := work.Name()
	workRoot := filepath.Dir(workPath)
	if err := work.Close(); err != nil {
		os.RemoveAll(workRoot)
		return "", nil, err
	}
	if err := os.Remove(workPath); err != nil {
		os.RemoveAll(workRoot)
		return "", nil, err
	}
	src := filepath.Join(workRoot, "export-src")
	cleanupSource := func() { _ = os.RemoveAll(workRoot) }
	if err := os.MkdirAll(filepath.Join(src, "items"), 0o700); err != nil {
		cleanupSource()
		return "", nil, err
	}
	manifest := Manifest{Version: FormatVersion, Counts: map[string]int{}}
	for _, item := range items {
		payloadFile := itemPayloadFile{
			ItemType: item.Meta.ItemType,
			Tags:     item.Tags,
			Payload:  mustMarshal(item.Payload),
		}
		raw, err := json.Marshal(payloadFile)
		if err != nil {
			cleanupSource()
			return "", nil, fmt.Errorf("%w: export failed", archive.ErrBadArchive)
		}
		path := fmt.Sprintf("items/%s.json", item.Meta.ID)
		if err := os.WriteFile(filepath.Join(src, path), raw, 0o600); err != nil {
			cleanupSource()
			return "", nil, fmt.Errorf("%w: export failed", archive.ErrBadArchive)
		}
		manifest.Files = append(manifest.Files, ManifestFile{
			ID:     item.Meta.ID,
			Type:   item.Meta.ItemType,
			Scope:  item.Meta.Scope,
			Path:   path,
			SHA256: sha256Hex(raw),
		})
		manifest.Counts[item.Meta.ItemType]++
	}
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		cleanupSource()
		return "", nil, fmt.Errorf("%w: export failed", archive.ErrBadArchive)
	}
	if err := os.WriteFile(filepath.Join(src, "manifest.json"), manifestRaw, 0o600); err != nil {
		cleanupSource()
		return "", nil, fmt.Errorf("%w: export failed", archive.ErrBadArchive)
	}
	archivePath, err := archive.Create(ctx, archive.CreateOptions{
		SourceDir: src, WorkDir: s.workDir, Passphrase: input.Passphrase,
	})
	if err != nil {
		cleanupSource()
		return "", nil, err
	}
	if info, err := os.Stat(archivePath); err != nil {
		os.RemoveAll(filepath.Dir(archivePath))
		cleanupSource()
		return "", nil, err
	} else if info.Size() > archive.MaxArchiveBytes {
		os.RemoveAll(filepath.Dir(archivePath))
		cleanupSource()
		return "", nil, archive.ErrTooLarge
	}
	// Cleanup drops the archive, its workspace and the staged source tree.
	cleanup := func() {
		os.RemoveAll(filepath.Dir(archivePath))
		cleanupSource()
	}
	if err := s.audit.Record(ctx, s.db, audit.Event{
		Name: audit.EventTransferExported, ActorID: actor.UserID,
		TargetType: audit.TargetUser, TargetID: actor.UserID, Result: audit.ResultSuccess,
	}); err != nil {
		cleanup()
		return "", nil, err
	}
	return archivePath, cleanup, nil
}

// PreviewResult is the confirmation payload shown to the user before any
// database write happens.
type PreviewResult struct {
	Token             string         `json:"preview_token"`
	Counts            map[string]int `json:"counts"`
	Conflicts         int            `json:"conflicts"`
	MissingReferences []string       `json:"missing_references"`
}

// Preview validates an uploaded archive and returns the import preview.
// Zero database writes happen here (design §11.4): the decrypted payloads
// are staged in the restricted work dir, keyed by the signed token.
func (s *Service) Preview(ctx context.Context, actor *auth.Principal, archiveBytes []byte, passphrase string) (PreviewResult, error) {
	result := PreviewResult{MissingReferences: []string{}}
	if actor == nil {
		return result, auth.ErrUnauthorized
	}
	if len(archiveBytes) > archive.MaxArchiveBytes {
		return result, archive.ErrTooLarge
	}
	if len(passphrase) > MaxPassphraseBytes {
		return result, fmt.Errorf("%w: passphrase exceeds the allowed size", ErrInvalidInput)
	}
	work, err := archive.TempFileUnder(s.workDir)
	if err != nil {
		return result, err
	}
	workPath := work.Name()
	workRoot := filepath.Dir(workPath)
	if err := work.Close(); err != nil {
		os.RemoveAll(workRoot)
		return result, err
	}
	uploadPath := workPath + ".7z"
	defer os.RemoveAll(workRoot)
	if err := os.Remove(workPath); err != nil {
		return result, err
	}
	if err := os.WriteFile(uploadPath, archiveBytes, 0o600); err != nil {
		return result, err
	}

	dest, entries, err := archive.Extract(ctx, uploadPath, passphrase, s.workDir)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dest)

	manifestRaw, err := os.ReadFile(filepath.Join(dest, "manifest.json"))
	if err != nil {
		return result, fmt.Errorf("%w: manifest missing", archive.ErrBadArchive)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return result, fmt.Errorf("%w: manifest unreadable", archive.ErrBadArchive)
	}
	if err := ManifestSelfCheck(manifest); err != nil {
		return result, fmt.Errorf("%w: %s", archive.ErrBadArchive, err.Error())
	}

	// Verify digests and decode the payloads; count conflicts and reference
	// gaps. Nothing touches the database.
	staged := make([]vault.ImportItem, 0, len(manifest.Files))
	result.Counts = map[string]int{}
	for _, f := range manifest.Files {
		relativePath, err := checkedManifestPath(f)
		if err != nil {
			return result, fmt.Errorf("%w: manifest entry rejected", archive.ErrBadArchive)
		}
		raw, err := os.ReadFile(filepath.Join(dest, relativePath))
		if err != nil {
			return result, fmt.Errorf("%w: payload file missing", archive.ErrBadArchive)
		}
		if sha256Hex(raw) != f.SHA256 {
			return result, fmt.Errorf("%w: payload digest mismatch", archive.ErrBadArchive)
		}
		var pf itemPayloadFile
		if err := json.Unmarshal(raw, &pf); err != nil {
			return result, fmt.Errorf("%w: payload unreadable", archive.ErrBadArchive)
		}
		if pf.ItemType != f.Type {
			return result, fmt.Errorf("%w: payload type mismatch", archive.ErrBadArchive)
		}
		if err := vault.ValidatePayload(f.Type, pf.Payload); err != nil {
			return result, fmt.Errorf("%w: %s", archive.ErrBadArchive, err.Error())
		}
		staged = append(staged, vault.ImportItem{
			OriginalID: f.ID, ItemType: f.Type, Scope: f.Scope, Tags: pf.Tags, Payload: pf.Payload,
		})
		result.Counts[f.Type]++
	}
	// Reference check runs after all IDs are known (two passes).
	archivedIDs := map[string]bool{}
	for _, item := range staged {
		archivedIDs[item.OriginalID] = true
	}
	for _, item := range staged {
		if ref, ok := billingRef(item.Payload); ok && !archivedIDs[ref] {
			result.MissingReferences = append(result.MissingReferences, ref)
		}
	}
	// Conflicts: IDs that already exist for anyone.
	for _, item := range staged {
		if s.vault.IDExists(ctx, item.OriginalID) {
			result.Conflicts++
		}
	}
	_ = entries

	// Stage the payloads for the confirm step and sign the token.
	tokenPayload, err := json.Marshal(staged)
	if err != nil {
		return result, err
	}
	expiry := s.now().Add(PreviewTTL)
	nonce, err := newPreviewNonce()
	if err != nil {
		return result, err
	}
	token, err := s.signToken(actor.UserID, actor.Session.ID, nonce, tokenPayload, expiry)
	if err != nil {
		return result, err
	}
	stageDir := filepath.Join(s.workDir, "preview-"+digest(token))
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return result, err
	}
	stageCommitted := false
	defer func() {
		if !stageCommitted {
			_ = os.RemoveAll(stageDir)
		}
	}()
	meta := struct {
		Actor   string `json:"actor"`
		Session string `json:"session,omitempty"`
		Nonce   string `json:"nonce"`
		Expiry  string `json:"expiry"`
	}{Actor: actor.UserID, Session: actor.Session.ID, Nonce: nonce, Expiry: expiry.UTC().Format(time.RFC3339Nano)}
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(stageDir, "meta.json"), metaRaw, 0o600); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(stageDir, "payloads.json"), tokenPayload, 0o600); err != nil {
		return result, err
	}
	result.Token = token
	stageCommitted = true
	s.sweepExpiredPreviews()
	return result, nil
}

// Confirm commits a previewed import in one transaction. The token must be
// fresh, unspent and belong to the caller.
func (s *Service) Confirm(ctx context.Context, actor *auth.Principal, token string) (int, int, error) {
	if actor == nil {
		return 0, 0, auth.ErrUnauthorized
	}
	stageDir := filepath.Join(s.workDir, "preview-"+digest(token))
	metaRaw, err := os.ReadFile(filepath.Join(stageDir, "meta.json"))
	if err != nil {
		return 0, 0, fmt.Errorf("%w: preview expired or unknown", archive.ErrBadArchive)
	}
	var meta struct {
		Actor   string `json:"actor"`
		Session string `json:"session"`
		Nonce   string `json:"nonce"`
		Expiry  string `json:"expiry"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return 0, 0, fmt.Errorf("%w: preview metadata unreadable", archive.ErrBadArchive)
	}
	if meta.Actor != actor.UserID {
		return 0, 0, fmt.Errorf("%w: preview belongs to another caller", archive.ErrBadArchive)
	}
	if meta.Session != "" && meta.Session != actor.Session.ID {
		return 0, 0, fmt.Errorf("%w: preview belongs to another session", archive.ErrBadArchive)
	}
	expiry, err := time.Parse(time.RFC3339Nano, meta.Expiry)
	if err != nil || s.now().After(expiry) {
		return 0, 0, fmt.Errorf("%w: preview expired", archive.ErrBadArchive)
	}
	// Rename is the consumption claim. It is atomic within the restricted
	// staging filesystem, so concurrent confirms cannot both reach ImportAll.
	claimDir, err := claimPreview(stageDir)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: preview expired or already consumed", archive.ErrBadArchive)
	}
	consumed := false
	defer func() {
		if !consumed {
			// ImportAll is transactional. Restore the stage after a pre-import
			// failure so a transient DB failure can be retried safely.
			_ = releasePreview(claimDir, stageDir)
		}
	}()
	if meta.Session != "" {
		if err := s.requireLiveSession(ctx, meta.Actor, meta.Session); err != nil {
			return 0, 0, err
		}
	}
	payloadRaw, err := os.ReadFile(filepath.Join(claimDir, "payloads.json"))
	if err != nil {
		return 0, 0, fmt.Errorf("%w: preview payloads missing", archive.ErrBadArchive)
	}
	if err := s.verifyToken(actor.UserID, actor.Session.ID, meta.Nonce, token, expiry, payloadRaw); err != nil {
		return 0, 0, err
	}
	var items []vault.ImportItem
	if err := json.Unmarshal(payloadRaw, &items); err != nil {
		return 0, 0, fmt.Errorf("%w: preview payloads unreadable", archive.ErrBadArchive)
	}
	if meta.Session != "" {
		// Recheck immediately before the write path. No transaction or file
		// lock is held while this database query waits.
		if err := s.requireLiveSession(ctx, meta.Actor, meta.Session); err != nil {
			return 0, 0, err
		}
	}
	var hook vault.ImportTxHook
	if meta.Session != "" {
		hook = func(hookCtx context.Context, tx *sql.Tx) error {
			return s.requireLiveSessionTx(hookCtx, tx, meta.Actor, meta.Session)
		}
	}
	imported, remapped, err := s.vault.ImportAllWithHook(ctx, actor, items, hook)
	if err != nil {
		if errors.Is(err, vault.ErrImportCommitUnknown) {
			// Commit may have succeeded despite returning an error. Never restore
			// the claim or invite a retry that could duplicate imported rows.
			consumed = true
			_ = os.RemoveAll(claimDir)
			return 0, 0, err
		}
		return 0, 0, err
	}
	// The database transaction committed successfully; this claim must never
	// be restored, even if cleanup or audit reporting fails, or a retry could
	// duplicate the import.
	consumed = true
	if err := os.RemoveAll(claimDir); err != nil {
		return imported, remapped, err
	}
	return imported, remapped, nil
}

// CancelPreview explicitly discards a staged preview before confirmation.
// The operation is owner- and (when available) session-bound just like
// Confirm. A concurrent confirm wins via the same atomic rename and cannot be
// canceled halfway through its import.
func (s *Service) CancelPreview(ctx context.Context, actor *auth.Principal, token string) error {
	if actor == nil {
		return auth.ErrUnauthorized
	}
	stageDir := filepath.Join(s.workDir, "preview-"+digest(token))
	meta, err := readPreviewMeta(stageDir)
	if err != nil {
		return fmt.Errorf("%w: preview expired or unknown", archive.ErrBadArchive)
	}
	if meta.Actor != actor.UserID || (meta.Session != "" && meta.Session != actor.Session.ID) {
		return fmt.Errorf("%w: preview belongs to another caller", archive.ErrBadArchive)
	}
	if expiry, err := time.Parse(time.RFC3339Nano, meta.Expiry); err != nil || s.now().After(expiry) {
		return fmt.Errorf("%w: preview expired", archive.ErrBadArchive)
	}
	claimDir, err := claimPreview(stageDir)
	if err != nil {
		return fmt.Errorf("%w: preview expired or already consumed", archive.ErrBadArchive)
	}
	return os.RemoveAll(claimDir)
}

// InvalidateSession removes previews tied to a session that has just been
// logged out or revoked. It is safe to call as a best-effort lifecycle hook;
// the periodic sweep remains the backstop for invalidated sessions.
func (s *Service) InvalidateSession(_ context.Context, actorID, sessionID string) {
	entries, err := os.ReadDir(s.workDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "preview-") {
			continue
		}
		meta, err := readPreviewMeta(filepath.Join(s.workDir, e.Name()))
		if err == nil && meta.Actor == actorID && meta.Session == sessionID {
			_ = os.RemoveAll(filepath.Join(s.workDir, e.Name()))
		}
	}
}

// InvalidateUser is used for administrator session revocation and account
// disable/delete operations, which invalidate every session at once.
func (s *Service) InvalidateUser(_ context.Context, actorID string) {
	entries, err := os.ReadDir(s.workDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "preview-") {
			continue
		}
		path := filepath.Join(s.workDir, e.Name())
		meta, err := readPreviewMeta(path)
		if err == nil && meta.Actor == actorID {
			_ = os.RemoveAll(path)
		}
	}
}

// signToken binds the caller, the staged payload digest and the expiry into
// one HMAC value — the token cannot be forged or replayed by another user.
func (s *Service) signToken(actorID, sessionID, nonce string, payload []byte, expiry time.Time) (string, error) {
	mac := hmac.New(sha256.New, s.hmacKey)
	fmt.Fprintf(mac, "%s\n%s\n%s\n%d\n", actorID, sessionID, nonce, expiry.Unix())
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) verifyToken(actorID, sessionID, nonce, token string, expiry time.Time, payload []byte) error {
	expected, err := s.signToken(actorID, sessionID, nonce, payload, expiry)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(token)) != 1 {
		return fmt.Errorf("%w: preview token mismatch", archive.ErrBadArchive)
	}
	return nil
}

func newPreviewNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

type previewMeta struct {
	Actor   string `json:"actor"`
	Session string `json:"session"`
	Nonce   string `json:"nonce"`
	Expiry  string `json:"expiry"`
}

func readPreviewMeta(stageDir string) (previewMeta, error) {
	raw, err := os.ReadFile(filepath.Join(stageDir, "meta.json"))
	if err != nil {
		return previewMeta{}, err
	}
	var meta previewMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return previewMeta{}, err
	}
	return meta, nil
}

// claimPreview uses one same-filesystem rename as the one-shot claim. Unlike
// a long-held flock, it does not occupy a database connection while import
// work waits for SQLite's writer lock.
func claimPreview(stageDir string) (string, error) {
	base := filepath.Base(stageDir)
	if !strings.HasPrefix(base, "preview-") {
		return "", fmt.Errorf("invalid preview path")
	}
	claimDir := filepath.Join(filepath.Dir(stageDir), "consuming-"+strings.TrimPrefix(base, "preview-"))
	if err := os.Rename(stageDir, claimDir); err != nil {
		return "", err
	}
	return claimDir, nil
}

func releasePreview(claimDir, stageDir string) error {
	if _, err := os.Stat(stageDir); err == nil {
		return nil
	}
	return os.Rename(claimDir, stageDir)
}

// requireLiveSession fails closed when the auth database cannot be checked;
// a transient query error must not turn a revoked preview into a valid one.
func (s *Service) requireLiveSession(ctx context.Context, actorID, sessionID string) error {
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.requireLiveSessionQuery(queryCtx, s.db, actorID, sessionID)
}

func (s *Service) requireLiveSessionTx(ctx context.Context, tx *sql.Tx, actorID, sessionID string) error {
	return s.requireLiveSessionQuery(ctx, tx, actorID, sessionID)
}

type rowQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Service) requireLiveSessionQuery(ctx context.Context, queryer rowQuery, actorID, sessionID string) error {
	if queryer == nil {
		return fmt.Errorf("transfer session check unavailable")
	}
	var live int
	at := s.now().UTC().Format(vault.TimestampFormat)
	err := queryer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s
		JOIN users u ON u.id=s.user_id
		WHERE s.public_id=? AND s.user_id=? AND s.revoked_at IS NULL
		  AND s.absolute_expires_at>? AND s.idle_expires_at>? AND u.status='active'`,
		sessionID, actorID, at, at).Scan(&live)
	if err != nil {
		if sqlite.IsBusy(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("transfer session check failed: %w", err)
	}
	if live != 1 {
		return fmt.Errorf("%w: preview session expired or revoked", archive.ErrBadArchive)
	}
	return nil
}

// sweepExpiredPreviews removes staged previews whose TTL has passed. Called
// on every preview; the scheduler can also call it.
func (s *Service) sweepExpiredPreviews() {
	entries, err := os.ReadDir(s.workDir)
	if err != nil {
		return
	}
	now := s.now()
	for _, e := range entries {
		isPreview := strings.HasPrefix(e.Name(), "preview-")
		isConsuming := strings.HasPrefix(e.Name(), "consuming-")
		if !isPreview && !isConsuming {
			continue
		}
		path := filepath.Join(s.workDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		// A consuming claim is never removed merely because its metadata is
		// temporarily unreadable: Confirm may be actively importing it. Only
		// reclaim a claim whose lease is clearly stale after a crashed process.
		if isConsuming {
			if now.Sub(info.ModTime()) > consumingLease {
				_ = os.RemoveAll(path)
			}
			continue
		}
		meta, err := readPreviewMeta(path)
		if err != nil {
			if now.Sub(info.ModTime()) > previewCreationGrace {
				_ = os.RemoveAll(path)
			}
			continue
		}
		expiry, err := time.Parse(time.RFC3339Nano, meta.Expiry)
		if err != nil {
			if now.Sub(info.ModTime()) > previewCreationGrace {
				_ = os.RemoveAll(path)
			}
			continue
		}
		if now.After(expiry) {
			_ = os.RemoveAll(path)
			continue
		}
		if meta.Session != "" {
			live, known := s.liveSession(meta.Actor, meta.Session)
			if known && !live {
				_ = os.RemoveAll(path)
				continue
			}
		}
		if _, err := os.Stat(filepath.Join(path, "payloads.json")); err != nil {
			if now.Sub(info.ModTime()) > previewCreationGrace {
				_ = os.RemoveAll(path)
			}
			continue
		}
	}
}

func (s *Service) removeStagedPreviews() {
	entries, err := os.ReadDir(s.workDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "preview-") || strings.HasPrefix(e.Name(), "consuming-") {
			_ = os.RemoveAll(filepath.Join(s.workDir, e.Name()))
		}
	}
}

// liveSession returns known=false on database errors so a periodic sweep
// retries rather than deleting a preview merely because the database was
// temporarily unavailable.
func (s *Service) liveSession(actorID, sessionID string) (live, known bool) {
	if s.db == nil {
		return false, false
	}
	var count int
	at := s.now().UTC().Format(vault.TimestampFormat)
	queryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.db.QueryRowContext(queryCtx, `SELECT COUNT(*) FROM sessions s
		JOIN users u ON u.id=s.user_id
		WHERE s.public_id=? AND s.user_id=? AND s.revoked_at IS NULL
		  AND s.absolute_expires_at>? AND s.idle_expires_at>? AND u.status='active'`,
		sessionID, actorID, at, at).Scan(&count)
	if err != nil {
		return false, false
	}
	return count == 1, true
}

// digest is the short hash used for preview directory names.
func digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func mustMarshal(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic("transfer: marshal: " + err.Error())
	}
	return raw
}

// billingRef extracts the billing address reference from a credit card
// payload, if any.
func billingRef(payload json.RawMessage) (string, bool) {
	var obj struct {
		BillingAddressItemID string `json:"billing_address_item_id"`
	}
	if json.Unmarshal(payload, &obj) != nil {
		return "", false
	}
	if obj.BillingAddressItemID == "" {
		return "", false
	}
	return obj.BillingAddressItemID, true
}
