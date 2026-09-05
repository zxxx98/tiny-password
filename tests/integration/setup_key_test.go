package integration

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
)

func TestSetupWithoutMasterKeyLeavesInstanceRetryable(t *testing.T) {
	db := openMigratedDB(t)
	logs := &capturedHandler{}
	logger := slog.New(logs)
	svc, err := bootstrap.NewService(db, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Options{Setup: &httpapi.SetupDeps{
		Service: svc, CSRF: httpapi.NewPreAuthCSRF(false), Logger: logger,
	}}))
	defer srv.Close()
	resp := setupRequest(t, srv.URL, logs.tokenValue(), "admin", validPassword)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable || body["code"] != "MAINTENANCE" {
		t.Errorf("missing key: status=%d body=%v", resp.StatusCode, body)
	}
	if svc.Initialized() {
		t.Error("missing key must not close setup")
	}
	var users, markers, successes int
	for query, count := range map[string]*int{
		"SELECT count(*) FROM users":                                      &users,
		"SELECT count(*) FROM system_state WHERE key='master_key_marker'": &markers,
		"SELECT count(*) FROM audit_events WHERE event='setup.success'":   &successes,
	} {
		if err := db.QueryRow(query).Scan(count); err != nil {
			t.Fatal(err)
		}
	}
	if users != 0 || markers != 0 || successes != 0 {
		t.Fatalf("setup wrote state: users=%d markers=%d successes=%d", users, markers, successes)
	}
	// A restart with a valid secret must still allow normal setup.
	h := newSetupHarnessOnDB(t, db)
	resp = setupRequest(t, h.server.URL, h.logger.tokenValue(), "admin", validPassword)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry after mounting key: %d", resp.StatusCode)
	}
}

func TestMasterKeyCheckRejectsMissingMarkerAfterInitialization(t *testing.T) {
	h := newSetupHarness(t)
	resp := setupRequest(t, h.server.URL, h.logger.tokenValue(), "admin", validPassword)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("setup failed")
	}
	key, _ := crypto.NewMasterKey(bytes.Repeat([]byte{0x11}, 32))
	other, _ := crypto.NewMasterKey(bytes.Repeat([]byte{0x22}, 32))
	if err := bootstrap.MasterKeyCheck(h.db.DB, key, nil)(); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.MasterKeyCheck(h.db.DB, other, nil)(); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := h.db.Exec("DELETE FROM system_state WHERE key='master_key_marker'"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []*crypto.MasterKey{key, other} {
		if err := bootstrap.MasterKeyCheck(h.db.DB, k, nil)(); err == nil {
			t.Error("initialized instance accepted key without marker")
		}
	}
}

func TestMasterKeyCheckRequiresKeyOnEmptyInstance(t *testing.T) {
	db := openMigratedDB(t)
	if err := bootstrap.MasterKeyCheck(db.DB, nil, nil)(); err == nil {
		t.Error("nil key accepted")
	}
	key, _ := crypto.NewMasterKey(bytes.Repeat([]byte{0x11}, 32))
	if err := bootstrap.MasterKeyCheck(db.DB, key, nil)(); err != nil {
		t.Fatal(err)
	}
}
