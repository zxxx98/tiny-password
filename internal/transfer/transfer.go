package transfer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// PreviewTTL bounds how long a preview stays confirmable (decision: short
// window bound to the caller).
const PreviewTTL = 10 * time.Minute

// Service orchestrates personal export/import on top of the vault service
// and the pinned archive adapter.
type Service struct {
	vault   *vault.Service
	audit   *audit.Service
	db      *sql.DB
	workDir string
	hmacKey []byte
	now     func() time.Time
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
	if err := os.RemoveAll(work.Name()); err != nil {
		return "", nil, err
	}
	src := filepath.Join(filepath.Dir(work.Name()), "export-src")
	if err := os.MkdirAll(filepath.Join(src, "items"), 0o700); err != nil {
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
			return "", nil, fmt.Errorf("%w: export failed", archive.ErrBadArchive)
		}
		path := fmt.Sprintf("items/%s.json", item.Meta.ID)
		if err := os.WriteFile(filepath.Join(src, path), raw, 0o600); err != nil {
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
		return "", nil, fmt.Errorf("%w: export failed", archive.ErrBadArchive)
	}
	if err := os.WriteFile(filepath.Join(src, "manifest.json"), manifestRaw, 0o600); err != nil {
		return "", nil, fmt.Errorf("%w: export failed", archive.ErrBadArchive)
	}
	archivePath, err := archive.Create(ctx, archive.CreateOptions{
		SourceDir: src, WorkDir: s.workDir, Passphrase: input.Passphrase,
	})
	if err != nil {
		os.RemoveAll(src)
		return "", nil, err
	}
	// Cleanup drops the archive, its workspace and the staged source tree.
	cleanup := func() {
		os.RemoveAll(filepath.Dir(archivePath))
		os.RemoveAll(src)
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
	work, err := archive.TempFileUnder(s.workDir)
	if err != nil {
		return result, err
	}
	uploadPath := work.Name() + ".7z"
	if err := os.WriteFile(uploadPath, archiveBytes, 0o600); err != nil {
		return result, err
	}
	defer os.RemoveAll(filepath.Dir(uploadPath))

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
		raw, err := os.ReadFile(filepath.Join(dest, f.Path))
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
	token, err := s.signToken(actor.UserID, tokenPayload, expiry)
	if err != nil {
		return result, err
	}
	stageDir := filepath.Join(s.workDir, "preview-"+digest(token))
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(stageDir, "payloads.json"), tokenPayload, 0o600); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(stageDir, "meta.json"),
		[]byte(fmt.Sprintf(`{"actor":%q,"expiry":%q}`, actor.UserID, expiry.UTC().Format(time.RFC3339Nano))), 0o600); err != nil {
		return result, err
	}
	result.Token = token
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
		Actor  string `json:"actor"`
		Expiry string `json:"expiry"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return 0, 0, fmt.Errorf("%w: preview metadata unreadable", archive.ErrBadArchive)
	}
	if meta.Actor != actor.UserID {
		return 0, 0, fmt.Errorf("%w: preview belongs to another caller", archive.ErrBadArchive)
	}
	expiry, err := time.Parse(time.RFC3339Nano, meta.Expiry)
	if err != nil || s.now().After(expiry) {
		return 0, 0, fmt.Errorf("%w: preview expired", archive.ErrBadArchive)
	}
	payloadRaw, err := os.ReadFile(filepath.Join(stageDir, "payloads.json"))
	if err != nil {
		return 0, 0, fmt.Errorf("%w: preview payloads missing", archive.ErrBadArchive)
	}
	if err := s.verifyToken(actor.UserID, token, expiry, payloadRaw); err != nil {
		return 0, 0, err
	}
	var items []vault.ImportItem
	if err := json.Unmarshal(payloadRaw, &items); err != nil {
		return 0, 0, fmt.Errorf("%w: preview payloads unreadable", archive.ErrBadArchive)
	}
	imported, remapped, err := s.vault.ImportAll(ctx, actor, items)
	if err != nil {
		return 0, 0, err
	}
	// The preview is single-use: drop the staged payloads.
	os.RemoveAll(stageDir)
	if err := s.audit.Record(ctx, s.db, audit.Event{
		Name: audit.EventTransferImported, ActorID: actor.UserID,
		TargetType: audit.TargetUser, TargetID: actor.UserID, Result: audit.ResultSuccess,
	}); err != nil {
		return imported, remapped, err
	}
	return imported, remapped, nil
}

// signToken binds the caller, the staged payload digest and the expiry into
// one HMAC value — the token cannot be forged or replayed by another user.
func (s *Service) signToken(actorID string, payload []byte, expiry time.Time) (string, error) {
	mac := hmac.New(sha256.New, s.hmacKey)
	fmt.Fprintf(mac, "%s\n%x\n%d\n", actorID, payload, expiry.Unix())
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) verifyToken(actorID, token string, expiry time.Time, payload []byte) error {
	expected, err := s.signToken(actorID, payload, expiry)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(token)) != 1 {
		return fmt.Errorf("%w: preview token mismatch", archive.ErrBadArchive)
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
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "preview-") {
			continue
		}
		metaRaw, err := os.ReadFile(filepath.Join(s.workDir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var meta struct {
			Expiry string `json:"expiry"`
		}
		if json.Unmarshal(metaRaw, &meta) != nil {
			continue
		}
		expiry, err := time.Parse(time.RFC3339Nano, meta.Expiry)
		if err == nil && s.now().After(expiry) {
			os.RemoveAll(filepath.Join(s.workDir, e.Name()))
		}
	}
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
