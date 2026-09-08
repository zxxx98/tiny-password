package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tiny-password/tiny-password/internal/platform/objectstore"
	"github.com/tiny-password/tiny-password/internal/settings"
)

// DefaultR2Prefix namespaces objects when the admin settings omit a prefix.
const DefaultR2Prefix = "tiny-password"

const (
	R2AccessKeyEnv = "TP_R2_ACCESS_KEY"
	R2SecretKeyEnv = "TP_R2_SECRET_KEY"
)

// ReadOptionalSecretFile reads a backup-domain secret file (backup
// passphrase, R2 credentials) that may legitimately be absent. An absent
// file yields an empty value; a present-but-unreadable file is an error —
// the operator must fix the mount rather than run with partial
// credentials.
func ReadOptionalSecretFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func readR2Credential(envName, filePath string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value, nil
	}
	return ReadOptionalSecretFile(filePath)
}

// NewSettingsR2Resolver combines the admin-maintained non-sensitive R2
// settings (endpoint, bucket, prefix) with the credential secret files
// (T22/T27: the database never stores credentials; secrets never enter the
// settings store). The resolver is evaluated on every run, so settings and
// remounted secrets take effect without a restart.
//
// It returns nil (target not configured) when either half is missing. A
// present-but-unreadable credential file is an error: the operator must
// fix the mount rather than run with partial credentials.
func NewSettingsR2Resolver(db *sql.DB, svc *settings.Service, accessKeyPath, secretKeyPath string) R2Resolver {
	return func(ctx context.Context) (*R2Delivery, error) {
		st, err := svc.Get(ctx)
		if err != nil {
			return nil, fmt.Errorf("read r2 settings: %w", err)
		}
		access, err := readR2Credential(R2AccessKeyEnv, accessKeyPath)
		if err != nil {
			return nil, err
		}
		secret, err := readR2Credential(R2SecretKeyEnv, secretKeyPath)
		if err != nil {
			return nil, err
		}
		if st.R2Endpoint == "" || st.R2Bucket == "" || access == "" || secret == "" {
			return nil, nil
		}
		client, err := objectstore.NewClient(objectstore.Config{
			Endpoint:        st.R2Endpoint,
			Region:          "auto",
			Bucket:          st.R2Bucket,
			AccessKeyID:     access,
			SecretAccessKey: secret,
		})
		if err != nil {
			return nil, fmt.Errorf("build r2 client: %w", err)
		}
		prefix := st.R2Prefix
		if prefix == "" {
			prefix = DefaultR2Prefix
		}
		return &R2Delivery{Client: client, Prefix: prefix}, nil
	}
}

// R2CredentialsPresent reports whether both R2 credentials resolve to
// readable, non-empty values (presence only — never the values).
func R2CredentialsPresent(accessKeyPath, secretKeyPath string) bool {
	access, err := readR2Credential(R2AccessKeyEnv, accessKeyPath)
	if err != nil || access == "" {
		return false
	}
	secret, err := readR2Credential(R2SecretKeyEnv, secretKeyPath)
	return err == nil && secret != ""
}
