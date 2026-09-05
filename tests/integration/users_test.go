package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/users"
)

// usersHarness wires the full router with member management enabled.
type usersHarness struct {
	*authHarness
	users       *users.Service
	idem        *idempotency.Service
	fault       atomic.Bool
	admin       authClient
	boot        *bootstrap.Service
	setupLogger *capturedHandler
}

func newUsersHarness(t *testing.T) *usersHarness {
	t.Helper()
	h := newAuthHarness(t)
	uh := &usersHarness{authHarness: h}
	idem, err := idempotency.NewService(h.db.DB, idempotency.Options{MACKey: newIdempotencyKeyMaterial()})
	if err != nil {
		t.Fatal(err)
	}
	uh.idem = idem

	authSvc, err := auth.NewService(h.db.DB, auth.Options{
		Now:    h.clock.Now,
		Limits: auth.Limits{Username: 1000, Source: 1000, Global: 1000},
		Audit: audit.NewService(audit.Options{Fault: func() error {
			if uh.fault.Load() {
				return auditFaultError{}
			}
			return nil
		}}),
		Logger: slogDiscard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	uh.users = users.NewService(h.db.DB, users.Options{
		Now: h.clock.Now,
		Audit: audit.NewService(audit.Options{Fault: func() error {
			if uh.fault.Load() {
				return auditFaultError{}
			}
			return nil
		}}),
	})
	cursor, err := httpapi.NewCursorCodec(bytes.Repeat([]byte{0x44}, 32), 0)
	if err != nil {
		t.Fatal(err)
	}
	// The setup endpoints also register /csrf, which the login helper needs;
	// both dep groups must share one CSRF instance.
	logger := &capturedHandler{}
	boot, err := bootstrap.NewService(h.db, mustMasterKey(t), slog.New(logger))
	if err != nil {
		t.Fatal(err)
	}
	uh.boot = boot
	uh.setupLogger = logger
	auditSvc := audit.NewService(audit.Options{Fault: func() error {
		if uh.fault.Load() {
			return auditFaultError{}
		}
		return nil
	}})
	csrf := httpapi.NewPreAuthCSRF(false)
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		Setup: &httpapi.SetupDeps{Service: boot, CSRF: csrf, Logger: slogDiscard()},
		Auth:  &httpapi.AuthDeps{Service: authSvc, CSRF: csrf},
		Audit: &httpapi.AuditDeps{Service: auditSvc, DB: h.db, Cursor: cursor, Session: authSvc},
		Users: &httpapi.UsersDeps{Service: uh.users, Session: authSvc, Cursor: cursor, Idempotency: uh.idem},
	}))
	t.Cleanup(srv.Close)
	h.svc = authSvc
	h.server = srv
	return uh
}

// bootstrapAdmin runs the real one-time setup so the harness has an admin.
func (h *usersHarness) bootstrapAdmin(t *testing.T) {
	t.Helper()
	token := h.setupLogger.tokenValue()
	if token == "" {
		t.Fatal("no setup token logged")
	}
	if _, err := h.boot.Initialize(bootstrap.InitializeInput{Token: token, Username: "Admin", Password: validPassword}); err != nil {
		t.Fatal(err)
	}
	h.admin, _ = h.login(t, "Admin", validPassword)
}

func TestUsersEndpointsRequireAdmin(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	h.user(t, "mallory", false)
	member, resp := h.login(t, "mallory", validPassword)
	expectStatus(t, resp, 200)

	paths := []struct{ method, path string }{
		{"GET", "/users"},
		{"POST", "/users"},
		{"POST", "/users/some-id/disable"},
		{"POST", "/users/some-id/enable"},
		{"POST", "/users/some-id/revoke-sessions"},
		{"DELETE", "/users/some-id"},
	}
	for _, p := range paths {
		resp := h.request(t, p.method, p.path, map[string]string{"username": "x", "initial_password": validPassword}, member)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s as member: %d, want 403", p.method, p.path, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Unauthenticated callers are rejected before authorization.
	resp = h.request(t, "GET", "/users", nil, authClient{})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUsersCreateValidationAndConflict(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)

	cases := []struct {
		name     string
		username string
		password string
		wantCode int
		want     int
	}{
		{"short username", "ab", validPassword, http.StatusBadRequest, 400},
		{"bad chars", "has space", validPassword, http.StatusBadRequest, 400},
		{"weak initial password", "worker", "short12", http.StatusBadRequest, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.request(t, "POST", "/users", map[string]string{
				"username": tc.username, "initial_password": tc.password,
			}, h.admin)
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status=%d", resp.StatusCode)
			}
		})
	}

	// Normalized uniqueness: display forms differ, normalized forms collide.
	resp := h.request(t, "POST", "/users", map[string]string{
		"username": "Worker", "initial_password": validPassword,
	}, h.admin)
	expectStatus(t, resp, 201)
	resp.Body.Close()
	resp = h.request(t, "POST", "/users", map[string]string{
		"username": "WORKER", "initial_password": validPassword,
	}, h.admin)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || body["code"] != "USERNAME_TAKEN" {
		t.Fatalf("conflict: %d %v", resp.StatusCode, body)
	}
}

func TestUsersCreateIdempotent(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	const key = "user-create-key-00001"
	const raceKey = "user-create-key-00002"

	create := func(username string) *http.Response {
		req := h.idemRequest(t, "POST", "/users", map[string]string{
			"username": username, "initial_password": validPassword,
		}, h.admin, key)
		return req
	}

	resp := create("idem-member")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create: %d", resp.StatusCode)
	}
	first := decodeBody(t, resp)

	// Same key + same body: replay, not USERNAME_TAKEN.
	resp = create("idem-member")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("replay: %d", resp.StatusCode)
	}
	replay := decodeBody(t, resp)
	if replay["id"] != first["id"] {
		t.Fatalf("replay id %v != original %v", replay["id"], first["id"])
	}

	// Same key, different content: conflict.
	resp = h.idemRequest(t, "POST", "/users", map[string]string{
		"username": "other-member", "initial_password": validPassword,
	}, h.admin, key)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || body["code"] != "IDEMPOTENCY_KEY_CONFLICT" {
		t.Fatalf("conflict: %d %v", resp.StatusCode, body)
	}

	// Concurrent identical creations under a fresh key: exactly one member.
	var wg sync.WaitGroup
	results := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := h.idemRequest(t, "POST", "/users", map[string]string{
				"username": "race-member", "initial_password": validPassword,
			}, h.admin, raceKey)
			results <- resp.StatusCode
			resp.Body.Close()
		}()
	}
	wg.Wait()
	close(results)
	created, other := 0, 0
	for code := range results {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			other++
		default:
			t.Fatalf("unexpected concurrent status %d", code)
		}
	}
	if created < 1 || created+other != 6 {
		t.Fatalf("created=%d conflicts=%d", created, other)
	}
	var members int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username_norm='race-member'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 1 {
		t.Fatalf("race members=%d, want exactly 1", members)
	}
}

func (h *usersHarness) idemRequest(t *testing.T, method, path string, body any, client authClient, key string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v1"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-CSRF-Token", client.csrf)
	req.Header.Set("Idempotency-Key", key)
	if client.cookie != nil {
		req.AddCookie(client.cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestMemberFirstLoginFlow(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)

	resp := h.request(t, "POST", "/users", map[string]string{
		"username": "newbie", "initial_password": "initial-pass-1234",
	}, h.admin)
	created := decodeBody(t, resp)
	if resp.StatusCode != http.StatusCreated || created["status"] != "must_change_password" {
		t.Fatalf("create: %d %v", resp.StatusCode, created)
	}

	member, resp := h.login(t, "newbie", "initial-pass-1234")
	expectStatus(t, resp, 200)

	// First-login restrictions: activity is gated.
	resp = h.request(t, "GET", "/auth/activity", nil, member)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("first-login activity: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Completing the change unlocks the member.
	expectStatus(t, h.request(t, "POST", "/auth/password", map[string]string{
		"current_password": "initial-pass-1234",
		"new_password":     "changed-pass-5678",
	}, member), 204)

	// After the change the member re-logs-in with the new password; the old
	// session is revoked.
	member, resp = h.login(t, "newbie", "changed-pass-5678")
	expectStatus(t, resp, 200)
	resp = h.request(t, "GET", "/auth/activity", nil, member)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activity after change: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The admin list reflects the active status.
	resp = h.request(t, "GET", "/users", nil, h.admin)
	body := decodeBody(t, resp)
	items := body["items"].([]any)
	var found map[string]any
	for _, it := range items {
		u := it.(map[string]any)
		if u["username"] == "newbie" {
			found = u
		}
	}
	if found == nil || found["status"] != "active" {
		t.Fatalf("member status after change: %v", found)
	}
}

func TestUsersDisableEnableSessions(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	h.user(t, "worker", false)
	member, resp := h.login(t, "worker", validPassword)
	expectStatus(t, resp, 200)
	memberID := h.lookupUserID(t, "worker")

	// Disable revokes sessions immediately.
	resp = h.request(t, "POST", "/users/"+memberID+"/disable", nil, h.admin)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusOK || body["status"] != "disabled" {
		t.Fatalf("disable: %d %v", resp.StatusCode, body)
	}
	resp = h.request(t, "GET", "/auth/session", nil, member)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after disable: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Login while disabled is a stable account-disabled error.
	_, resp = h.login(t, "worker", validPassword)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login while disabled: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Re-enable: old sessions stay dead, new logins work.
	resp = h.request(t, "POST", "/users/"+memberID+"/enable", nil, h.admin)
	body = decodeBody(t, resp)
	if resp.StatusCode != http.StatusOK || body["status"] != "active" {
		t.Fatalf("enable: %d %v", resp.StatusCode, body)
	}
	resp = h.request(t, "GET", "/auth/session", nil, member)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session resurrected after enable: %d", resp.StatusCode)
	}
	resp.Body.Close()
	_, resp = h.login(t, "worker", validPassword)
	expectStatus(t, resp, 200)

	// Disabling again is idempotent; the enable of an active member too.
	resp = h.request(t, "POST", "/users/"+memberID+"/disable", nil, h.admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("double disable: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = h.request(t, "POST", "/users/"+memberID+"/disable", nil, h.admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("triple disable: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLastAdminProtection(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	adminID := h.lookupUserID(t, "Admin")

	// The sole administrator cannot be disabled or deleted.
	resp := h.request(t, "POST", "/users/"+adminID+"/disable", nil, h.admin)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || body["code"] != "LAST_ADMIN_PROTECTED" {
		t.Fatalf("self disable: %d %v", resp.StatusCode, body)
	}
	resp = h.request(t, "DELETE", "/users/"+adminID, map[string]string{"confirm_username": "Admin"}, h.admin)
	body = decodeBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || body["code"] != "FORBIDDEN" {
		t.Fatalf("self delete: %d %v", resp.StatusCode, body)
	}

	// A second administrator allows disabling, but removing both is blocked.
	h.insertAdmin(t, "second-admin")
	secondID := h.lookupUserID(t, "second-admin")
	resp = h.request(t, "POST", "/users/"+secondID+"/disable", nil, h.admin)
	expectStatus(t, resp, 200)
	// With second-admin disabled, admin is again the last active admin: the
	// disabled one cannot act, so delete of admin by admin is self-delete.
	// Re-enable and delete instead.
	resp = h.request(t, "POST", "/users/"+secondID+"/enable", nil, h.admin)
	expectStatus(t, resp, 200)
	resp = h.request(t, "DELETE", "/users/"+secondID, map[string]string{"confirm_username": "second-admin"}, h.admin)
	expectStatus(t, resp, 204)

	// Service-level race: with race-a and race-b the ONLY active admins, two
	// concurrent deletes of each other must leave exactly one administrator.
	if _, err := h.db.Exec(`UPDATE users SET status='disabled' WHERE username_norm='admin'`); err != nil {
		t.Fatal(err)
	}
	h.insertAdmin(t, "race-a")
	h.insertAdmin(t, "race-b")
	a := h.adminPrincipal(t, "race-a")
	b := h.adminPrincipal(t, "race-b")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- h.users.Delete(t.Context(), a, b.UserID, users.DeleteInput{ConfirmUsername: "race-b"})
	}()
	go func() {
		defer wg.Done()
		errs <- h.users.Delete(t.Context(), b, a.UserID, users.DeleteInput{ConfirmUsername: "race-a"})
	}()
	wg.Wait()
	close(errs)
	okCount := 0
	for err := range errs {
		switch {
		case err == nil:
			okCount++
		case !errors.Is(err, auth.ErrUnauthorized) && !strings.Contains(err.Error(), "last administrator"):
			t.Fatalf("unexpected concurrent delete error: %v", err)
		}
	}
	if okCount != 1 {
		t.Fatalf("concurrent deletes: %d succeeded, want exactly 1", okCount)
	}
	var admins int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND status='active'`).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Fatalf("active admins remaining=%d, want exactly 1", admins)
	}
}

// memberRow inserts a user whose internal id differs from the username.
func (h *usersHarness) memberRow(t *testing.T, id, display string, force bool) {
	t.Helper()
	hash, err := auth.HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	ts := h.clock.Now().Format("2006-01-02T15:04:05.000000000Z")
	if _, err := h.db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at)
		VALUES(?,?,?,?, 'active', ?, ?, ?, ?)`, id, strings.ToLower(display), display, "member", force, hash, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func (h *usersHarness) insertAdmin(t *testing.T, name string) {
	t.Helper()
	hash, err := auth.HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	ts := h.clock.Now().Format("2006-01-02T15:04:05.000000000Z")
	if _, err := h.db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at)
		VALUES(?,?,?,?, 'active', 0, ?, ?, ?)`, "admin-"+name, name, name, "admin", hash, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func (h *usersHarness) adminPrincipal(t *testing.T, name string) *auth.Principal {
	t.Helper()
	result, err := h.svc.Login(t.Context(), name, validPassword, "test")
	if err != nil {
		t.Fatal(err)
	}
	return &result.Principal
}

func (h *authHarness) lookupUserID(t *testing.T, display string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(`SELECT id FROM users WHERE username_display=?`, display).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUsersDeleteConfirmationAndCascade(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	h.memberRow(t, "u-gone-1", "gone-soon", false)
	h.memberRow(t, "u-stayer-1", "stayer", false)
	goneID := "u-gone-1"
	stayerID := "u-stayer-1"

	seedUserFixtures(t, h.db, goneID, stayerID)

	// A live session of the doomed member.
	member, resp := h.login(t, "gone-soon", validPassword)
	expectStatus(t, resp, 200)

	// Wrong confirmation is rejected; case must match the display form.
	resp = h.request(t, "DELETE", "/users/"+goneID, map[string]string{"confirm_username": "wrong"}, h.admin)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || body["code"] != "CONFIRMATION_MISMATCH" {
		t.Fatalf("wrong confirm: %d %v", resp.StatusCode, body)
	}
	resp = h.request(t, "DELETE", "/users/"+goneID, map[string]string{"confirm_username": "GONE-SOON"}, h.admin)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("case-sensitive confirm: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Audit fault: the whole delete rolls back.
	h.fault.Store(true)
	resp = h.request(t, "DELETE", "/users/"+goneID, map[string]string{"confirm_username": "gone-soon"}, h.admin)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("delete under audit fault: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if !userExists(t, h.db, goneID) || countItems(t, h.db, goneID) != 2 {
		t.Fatalf("rollback failed: userExists=%v items=%d", userExists(t, h.db, goneID), countItems(t, h.db, goneID))
	}
	h.fault.Store(false)

	// Correct confirmation: delete cascades.
	resp = h.request(t, "DELETE", "/users/"+goneID, map[string]string{"confirm_username": "gone-soon"}, h.admin)
	expectStatus(t, resp, 204)

	if userExists(t, h.db, goneID) {
		t.Fatal("user survived delete")
	}
	// Sessions cascaded; the member's session is dead.
	resp = h.request(t, "GET", "/auth/session", nil, member)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("doomed member session alive: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Personal items and shared creations (with history) are gone.
	if got := countItems(t, h.db, goneID); got != 0 {
		t.Fatalf("items of deleted member remain: %d", got)
	}
	var versions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM item_versions v JOIN vault_items i ON i.id=v.item_id WHERE i.owner_user_id=? OR i.created_by_user_id=?`, goneID, goneID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 0 {
		t.Fatalf("history rows remain: %d", versions)
	}
	// Other members' data survives.
	if got := countItems(t, h.db, stayerID); got != 2 {
		t.Fatalf("stayer items=%d, want 2", got)
	}
	// The member's idempotency claims are gone; unrelated ones stay.
	var idem int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM idempotency_keys WHERE scope=?`, "items.create:"+goneID).Scan(&idem); err != nil {
		t.Fatal(err)
	}
	if idem != 0 {
		t.Fatal("deleted member's idempotency claims remain")
	}
	// Audit rows survive with the opaque id and no username snapshot.
	var auditCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE actor_id=?`, goneID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount == 0 {
		t.Fatal("audit rows of the deleted member were removed")
	}
	var snapshot int
	if err := h.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM audit_events WHERE actor_id='gone-soon' OR target_id='gone-soon')`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot != 0 {
		t.Fatal("audit stores a username snapshot")
	}
	// The delete itself is audited.
	var deleted int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event=? AND target_id=?`, audit.EventUserDeleted, goneID).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("user.deleted events=%d, want 1", deleted)
	}
}

func TestUsersRevokeSessionsEndpoint(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	h.user(t, "revoked-member", false)
	member, resp := h.login(t, "revoked-member", validPassword)
	expectStatus(t, resp, 200)
	memberID := h.lookupUserID(t, "revoked-member")

	resp = h.request(t, "POST", "/users/"+memberID+"/revoke-sessions", nil, h.admin)
	expectStatus(t, resp, 204)
	resp = h.request(t, "GET", "/auth/session", nil, member)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after admin revocation: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Status unchanged: the member can simply log in again.
	_, resp = h.login(t, "revoked-member", validPassword)
	expectStatus(t, resp, 200)
}

// seedUserFixtures inserts synthetic vault data proving the delete cascade:
// two personal items, two shared items and one history row.
func seedUserFixtures(t *testing.T, db *sqlite.DB, doomed, stayer string) {
	t.Helper()
	ts := "2026-09-05T08:00:00.000000000Z"
	insert := func(id, scope string, owner, creator any) {
		if _, err := db.Exec(`INSERT INTO vault_items(id, vault_scope, owner_user_id, created_by_user_id, item_type, favorite, payload_version, nonce, ciphertext, revision, created_at, updated_at)
			VALUES(?,?,?,?, 'login', 0, 1, x'000000000000000000000000000000000000000000000000', x'00', 1, ?, ?)`,
			id, scope, owner, creator, ts, ts); err != nil {
			t.Fatalf("seed item %s: %v", id, err)
		}
	}
	insert("item-doomed-personal", "personal", doomed, nil)
	insert("item-doomed-shared", "shared", nil, doomed)
	insert("item-stayer-personal", "personal", stayer, nil)
	insert("item-stayer-shared", "shared", nil, stayer)
	if _, err := db.Exec(`INSERT INTO item_versions(item_id, revision, payload_version, nonce, ciphertext, created_at)
		VALUES('item-doomed-personal', 1, 1, x'00', x'00', ?)`, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO idempotency_keys(scope, key, fingerprint, status, created_at, expires_at)
		VALUES(?, 'key-0000000000000001', 'fp', 'completed', ?, ?)`,
		"items.create:"+doomed, ts, "2026-09-06T08:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO idempotency_keys(scope, key, fingerprint, status, created_at, expires_at)
		VALUES(?, 'key-0000000000000002', 'fp', 'completed', ?, ?)`,
		"items.create:"+stayer, ts, "2026-09-06T08:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
}

func userExists(t *testing.T, db *sqlite.DB, id string) bool {
	t.Helper()
	var exists int
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE id=?)`, id).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists == 1
}

func countItems(t *testing.T, db *sqlite.DB, userID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vault_items WHERE owner_user_id=? OR created_by_user_id=?`, userID, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
