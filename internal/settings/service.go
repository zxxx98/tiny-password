// Package settings owns the non-sensitive runtime settings store (T27).
// Values live in app_settings under one namespaced JSON document; the
// update surface is an explicit whitelist — secrets (R2 access/secret keys,
// backup passphrase, master key) are injected via deployment configuration
// and never pass through, stored by, or echo out of this package.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// settingsKey is the single namespaced document.
const settingsKey = "settings.runtime"

// Settings is the complete whitelist of persisted, non-sensitive settings.
type Settings struct {
	R2Endpoint string `json:"r2_endpoint"`
	R2Bucket   string `json:"r2_bucket"`
	R2Prefix   string `json:"r2_prefix"`
}

var bucketPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,62}$`)
var prefixPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/._-]{0,120}$`)

// Validate enforces the whitelist value rules.
func (s Settings) Validate() error {
	if s.R2Endpoint != "" {
		u, err := url.Parse(s.R2Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("r2_endpoint must be an https URL")
		}
	}
	if s.R2Bucket != "" && !bucketPattern.MatchString(s.R2Bucket) {
		return errors.New("r2_bucket has an invalid form")
	}
	if s.R2Prefix != "" && !prefixPattern.MatchString(s.R2Prefix) {
		return errors.New("r2_prefix has an invalid form")
	}
	return nil
}

// Service reads and updates the whitelisted settings.
type Service struct {
	db  *sql.DB
	now func() time.Time
}

// NewService builds the settings service over the app pool.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now}
}

// Get returns the current settings (zero values when never configured).
func (s *Service) Get(ctx context.Context) (Settings, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM app_settings WHERE key = ?", settingsKey,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	var st Settings
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return Settings{}, fmt.Errorf("parse settings: %w", err)
	}
	return st, nil
}

// Update validates and persists the whitelist. Unknown or sensitive fields
// cannot even be expressed: the struct is the whitelist.
func (s *Service) Update(ctx context.Context, st Settings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		settingsKey, string(raw), s.now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store settings: %w", err)
	}
	return nil
}
