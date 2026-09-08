package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/settings"
	"github.com/tiny-password/tiny-password/migrations"
)

func newMigratedR2Settings(t *testing.T) *settings.Service {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := settings.NewService(db.DB)
	if err := svc.Update(context.Background(), settings.Settings{
		R2Endpoint: "https://account.example.com",
		R2Bucket:   "test-bucket",
		R2Prefix:   "backups/test",
	}); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestR2Resolver(t *testing.T) {
	tests := []struct {
		name       string
		accessEnv  string
		secretEnv  string
		accessFile func(t *testing.T, path string)
		secretFile func(t *testing.T, path string)
		wantReady  bool
	}{
		{name: "environment credentials", accessEnv: "env-access", secretEnv: "env-secret", wantReady: true},
		{
			name: "file fallback",
			accessFile: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("file-access\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			secretFile: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantReady: true,
		},
		{
			name:       "environment overrides files",
			accessEnv:  "env-access",
			secretEnv:  "env-secret",
			accessFile: func(t *testing.T, path string) { t.Helper(); makeDirectory(t, path) },
			secretFile: func(t *testing.T, path string) { t.Helper(); makeDirectory(t, path) },
			wantReady:  true,
		},
		{name: "only access credential", accessEnv: "env-access", wantReady: false},
		{name: "no credentials", wantReady: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TP_R2_ACCESS_KEY", tc.accessEnv)
			t.Setenv("TP_R2_SECRET_KEY", tc.secretEnv)
			accessPath := filepath.Join(t.TempDir(), "access")
			secretPath := filepath.Join(t.TempDir(), "secret")
			if tc.accessFile != nil {
				tc.accessFile(t, accessPath)
			}
			if tc.secretFile != nil {
				tc.secretFile(t, secretPath)
			}

			svc := newMigratedR2Settings(t)
			resolver := NewSettingsR2Resolver(nil, svc, accessPath, secretPath)
			delivery, err := resolver(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if (delivery != nil) != tc.wantReady {
				t.Fatalf("delivery ready = %t, want %t", delivery != nil, tc.wantReady)
			}
			if delivery != nil {
				if _, ok := any(delivery).(*R2Delivery); !ok {
					t.Fatalf("delivery type = %T, want *R2Delivery", delivery)
				}
			}
		})
	}
}

func TestR2CredentialsPresent(t *testing.T) {
	accessFile := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("file-access"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secretFile := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("file-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		accessEnv  string
		secretEnv  string
		accessFile func(t *testing.T, path string)
		secretFile func(t *testing.T, path string)
		want       bool
	}{
		{name: "environment credentials", accessEnv: "env-access", secretEnv: "env-secret", want: true},
		{name: "file fallback", accessFile: accessFile, secretFile: secretFile, want: true},
		{
			name:       "environment overrides files",
			accessEnv:  "env-access",
			secretEnv:  "env-secret",
			accessFile: func(t *testing.T, path string) { t.Helper(); makeDirectory(t, path) },
			secretFile: func(t *testing.T, path string) { t.Helper(); makeDirectory(t, path) },
			want:       true,
		},
		{name: "only access credential", accessEnv: "env-access", want: false},
		{name: "no credentials", want: false},
		{name: "whitespace environment falls back to files", accessEnv: " \t", secretEnv: "\n", accessFile: accessFile, secretFile: secretFile, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TP_R2_ACCESS_KEY", tc.accessEnv)
			t.Setenv("TP_R2_SECRET_KEY", tc.secretEnv)
			accessPath := filepath.Join(t.TempDir(), "access")
			secretPath := filepath.Join(t.TempDir(), "secret")
			if tc.accessFile != nil {
				tc.accessFile(t, accessPath)
			}
			if tc.secretFile != nil {
				tc.secretFile(t, secretPath)
			}

			if got := R2CredentialsPresent(accessPath, secretPath); got != tc.want {
				t.Fatalf("credentials present = %t, want %t", got, tc.want)
			}
		})
	}
}

func makeDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
