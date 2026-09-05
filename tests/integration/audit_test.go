package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
)

// auditHarness wires the full router with auditing enabled and injectable
// audit faults, plus a captured logger for leak assertions.
type auditHarness struct {
	*authHarness
	logger   *capturedHandler
	auditSvc *audit.Service
	fault    atomic.Bool
}

func newAuditHarness(t *testing.T) *auditHarness {
	t.Helper()
	h := newAuthHarness(t) // provides db, clock; its server is replaced below

	logger := &capturedHandler{}
	slogLogger := slog.New(logger)
	ah := &auditHarness{authHarness: h, logger: logger}
	ah.auditSvc = audit.NewService(audit.Options{Fault: func() error {
		if ah.fault.Load() {
			return auditFaultError{}
		}
		return nil
	}})
	authSvc, err := auth.NewService(h.db.DB, auth.Options{
		Now:    h.clock.Now,
		Limits: auth.Limits{Username: 1000, Source: 1000, Global: 1000},
		Audit:  ah.auditSvc,
		Logger: slogLogger,
	})
	if err != nil {
		t.Fatal(err)
	}
	boot, err := bootstrap.NewService(h.db, mustMasterKey(t), slogLogger)
	if err != nil {
		t.Fatal(err)
	}
	csrf := httpapi.NewPreAuthCSRF(false)
	cursor, err := httpapi.NewCursorCodec(bytes.Repeat([]byte{0x33}, 32), 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		Setup:  &httpapi.SetupDeps{Service: boot, CSRF: csrf, Logger: slogLogger},
		Auth:   &httpapi.AuthDeps{Service: authSvc, CSRF: csrf},
		Audit:  &httpapi.AuditDeps{Service: ah.auditSvc, DB: h.db, Cursor: cursor, Session: authSvc},
		Logger: slogLogger,
	}))
	t.Cleanup(srv.Close)
	h.svc = authSvc
	h.boot = boot
	h.server = srv
	return ah
}

func mustMasterKey(t *testing.T) *crypto.MasterKey {
	t.Helper()
	key, err := crypto.NewMasterKey(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type auditFaultError struct{}

func (auditFaultError) Error() string { return "injected audit failure" }

// auditRows returns all audit rows, newest first.
func (h *auditHarness) auditRows(t *testing.T) []audit.Entry {
	t.Helper()
	page, err := h.auditSvc.System(t.Context(), h.db.DB, "", "", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	return page.Entries
}

func (h *auditHarness) countEvents(t *testing.T, name string) int {
	t.Helper()
	n := 0
	for _, e := range h.auditRows(t) {
		if e.Event == name {
			n++
		}
	}
	return n
}

// memberRow inserts a user whose internal id differs from the username, so
// audit assertions can tell "stored the resolved id" from "stored the name".
func (h *auditHarness) memberRow(t *testing.T, id, display string, force bool) {
	t.Helper()
	hash, err := auth.HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	norm := strings.ToLower(display)
	ts := h.clock.Now().Format("2006-01-02T15:04:05.000000000Z")
	if _, err := h.db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, id, norm, display, "member", "active", force, hash, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func TestAuditLoginSuccessAndFailure(t *testing.T) {
	h := newAuditHarness(t)
	h.memberRow(t, "u-alice-1", "Alice", false)

	// Failed login for an existing user resolves the actor id.
	_, resp := h.login(t, "Alice", "definitely-wrong-password")
	expectStatus(t, resp, 401)
	if got := h.countEvents(t, audit.EventLoginFailure); got != 1 {
		t.Fatalf("login failure events=%d", got)
	}
	for _, e := range h.auditRows(t) {
		if e.Event == audit.EventLoginFailure {
			if e.ActorID != "u-alice-1" {
				t.Fatalf("failure event actor=%q: want resolved user id, never the username", e.ActorID)
			}
		}
	}

	// Failed login for an unknown user stays anonymous.
	_, resp = h.login(t, "ghost-user-xyz", "definitely-wrong-password")
	expectStatus(t, resp, 401)
	rows := h.auditRows(t)
	last := rows[0]
	if last.Event != audit.EventLoginFailure || last.ActorID != audit.Anonymous {
		t.Fatalf("unknown-user failure: event=%s actor=%q", last.Event, last.ActorID)
	}

	// Successful login shares the session transaction.
	client, resp := h.login(t, "Alice", validPassword)
	expectStatus(t, resp, 200)
	if got := h.countEvents(t, audit.EventLoginSuccess); got != 1 {
		t.Fatalf("login success events=%d", got)
	}

	// Logout is audited.
	expectStatus(t, h.request(t, "POST", "/auth/logout", nil, client), 204)
	if got := h.countEvents(t, audit.EventLogout); got != 1 {
		t.Fatalf("logout events=%d", got)
	}

	// Self-revoking another own session is audited with the revoked id.
	h.memberRow(t, "u-alice-2", "Alice2", false)
	c2, resp := h.login(t, "Alice2", validPassword)
	expectStatus(t, resp, 200)
	c2Session := loginSessionID(t, resp)
	c3, resp := h.login(t, "Alice2", validPassword)
	expectStatus(t, resp, 200)
	_ = loginSessionID(t, resp)

	expectStatus(t, h.request(t, "DELETE", "/auth/sessions/"+c2Session, nil, c3), 204)
	if got := h.countEvents(t, audit.EventSessionRevoked); got != 1 {
		t.Fatalf("session revoke events=%d", got)
	}
	for _, e := range h.auditRows(t) {
		if e.Event == audit.EventSessionRevoked && e.TargetID != c2Session {
			t.Fatalf("revoked target=%q want %q", e.TargetID, c2Session)
		}
	}
	// The revoked session can no longer be used; the revoker's still works.
	resp = h.request(t, "GET", "/auth/session", nil, c2)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session still live: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = h.request(t, "GET", "/auth/session", nil, c3)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoker session broken: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// loginSessionID extracts the new session's public id from a login response.
func loginSessionID(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		User struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.User.Session.ID == "" {
		t.Fatal("login response without session id")
	}
	return body.User.Session.ID
}

func TestAuditPasswordChangedInSameTransaction(t *testing.T) {
	h := newAuditHarness(t)
	h.user(t, "carol", false)
	client, resp := h.login(t, "carol", validPassword)
	expectStatus(t, resp, 200)

	expectStatus(t, h.request(t, "POST", "/auth/password", map[string]string{
		"current_password": validPassword,
		"new_password":     validPassword + "-changed",
	}, client), 204)

	if got := h.countEvents(t, audit.EventPasswordChanged); got != 1 {
		t.Fatalf("password change events=%d", got)
	}
}

func TestAuditWriteFailureRollsBackBusinessChange(t *testing.T) {
	h := newAuditHarness(t)
	h.user(t, "dave", false)

	// With the audit fault armed, a successful login cannot commit its
	// session: no audit row, no session row, uniform client error.
	h.fault.Store(true)
	_, resp := h.login(t, "dave", validPassword)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("login status=%d, want 500 when the success audit cannot be written", resp.StatusCode)
	}
	resp.Body.Close()
	var sessions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions=%d after failed login; the change must roll back with its audit", sessions)
	}
	if got := h.countEvents(t, audit.EventLoginSuccess); got != 0 {
		t.Fatalf("success audit rows=%d, want 0", got)
	}

	// Standalone failure records (no business change to protect) tolerate
	// write failures without breaking the error path.
	_, resp = h.login(t, "dave", "wrong-password-99")
	expectStatus(t, resp, 401)

	h.fault.Store(false)
	client, resp := h.login(t, "dave", validPassword)
	expectStatus(t, resp, 200)
	if got := h.countEvents(t, audit.EventLoginSuccess); got != 1 {
		t.Fatalf("recovered login events=%d", got)
	}

	// Password change with armed fault: rolled back atomically.
	h.fault.Store(true)
	resp = h.request(t, "POST", "/auth/password", map[string]string{
		"current_password": validPassword,
		"new_password":     "another-new-password-1",
	}, client)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("password change status=%d, want 500", resp.StatusCode)
	}
	resp.Body.Close()
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 { // only the login session survives
		t.Fatalf("live sessions=%d after failed change; old sessions must survive intact", sessions)
	}
}

func TestAuditPersonalActivityIsolation(t *testing.T) {
	h := newAuditHarness(t)
	h.memberRow(t, "u-erin-1", "Erin", false)
	h.memberRow(t, "u-frank-1", "Frank", false)

	erin, resp := h.login(t, "Erin", validPassword)
	expectStatus(t, resp, 200)
	_, resp = h.login(t, "Frank", validPassword)
	expectStatus(t, resp, 200)

	// Erin reads her own activity: only her events.
	resp = h.request(t, "GET", "/auth/activity", nil, erin)
	body := decodeBody(t, resp)
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("erin activity items=%d, want exactly her login event", len(items))
	}
	first := items[0].(map[string]any)
	if first["event"] != audit.EventLoginSuccess || first["actor_id"] != "u-erin-1" {
		t.Fatalf("event=%v actor=%v", first["event"], first["actor_id"])
	}

	// Members cannot read the system audit.
	resp = h.request(t, "GET", "/admin/audit", nil, erin)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member system audit status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// Admin reads all events with a filter: erin, frank and the admin's own
	// login make three.
	admin := h.directAdmin(t)
	resp = h.request(t, "GET", "/admin/audit?event=auth.login.success", nil, admin)
	body = decodeBody(t, resp)
	items = body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("admin filtered items=%d, want 3 logins", len(items))
	}
	resp = h.request(t, "GET", "/admin/audit?event=not-an-event", nil, admin)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid filter status=%d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAuditCursorBinding(t *testing.T) {
	h := newAuditHarness(t)
	h.user(t, "gina", false)
	h.user(t, "hank", false)

	for _, name := range []string{"gina", "hank", "gina", "hank", "gina", "hank"} {
		_, resp := h.login(t, name, validPassword)
		expectStatus(t, resp, 200)
	}
	admin := h.directAdmin(t)

	// Page 1.
	resp := h.request(t, "GET", "/admin/audit?limit=3", nil, admin)
	body := decodeBody(t, resp)
	items := body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("page1 items=%d", len(items))
	}
	next := body["next_cursor"].(string)

	// Page 2 continues without overlap.
	resp = h.request(t, "GET", "/admin/audit?limit=3&cursor="+url.QueryEscape(next), nil, admin)
	body = decodeBody(t, resp)
	page2 := body["items"].([]any)
	if len(page2) == 0 {
		t.Fatal("page2 empty")
	}
	seen := map[string]bool{}
	for _, it := range append(append([]any{}, items...), page2...) {
		id := it.(map[string]any)["id"].(string)
		if seen[id] {
			t.Fatal("cursor page overlap")
		}
		seen[id] = true
	}

	// A foreign actor's cursor is rejected; so are forged cursors.
	gina, resp := h.login(t, "gina", validPassword)
	expectStatus(t, resp, 200)
	resp = h.request(t, "GET", "/auth/activity?cursor="+url.QueryEscape(next), nil, gina)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign cursor status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = h.request(t, "GET", "/admin/audit?cursor="+url.QueryEscape(next+"x"), nil, admin)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("tampered cursor status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// Expired cursors are rejected (codec TTL is short in production; here we
	// rely on the codec unit tests for time travel — binding is the HTTP risk).
}

func TestAuditSurvivesUserDeletionWithoutUsernameSnapshot(t *testing.T) {
	h := newAuditHarness(t)
	h.memberRow(t, "u-ivy-1", "Ivy-Del", false)
	_, resp := h.login(t, "Ivy-Del", validPassword)
	expectStatus(t, resp, 200)

	// Delete the user directly (the admin API arrives with T09); audit rows
	// have no foreign key and must survive.
	if _, err := h.db.Exec(`DELETE FROM users WHERE username_norm='ivy-del'`); err != nil {
		t.Fatal(err)
	}
	rows := h.auditRows(t)
	if len(rows) == 0 {
		t.Fatal("audit rows lost after user deletion")
	}
	found := false
	for _, e := range rows {
		if e.ActorID == "u-ivy-1" {
			found = true
		}
		if e.ActorID == "" {
			t.Fatal("deleted user's events must keep the opaque actor id")
		}
	}
	if !found {
		t.Fatal("the deleted user's events are gone")
	}
	// No username snapshot (either form) anywhere in identity columns.
	var exists int
	if err := h.db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM audit_events WHERE actor_id IN ('ivy-del','Ivy-Del')
		   OR target_id IN ('ivy-del','Ivy-Del')
		   OR request_id IN ('ivy-del','Ivy-Del'))`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != 0 {
		t.Fatal("audit stores a username snapshot")
	}
}

func TestAuditNeverContainsSyntheticSecrets(t *testing.T) {
	h := newAuditHarness(t)
	token := h.logger.tokenValue()

	const failedPassword = "SYNSECRET-PW-4471-x"
	h.user(t, "jack", false)
	_, resp := h.login(t, "jack", failedPassword)
	expectStatus(t, resp, 401)
	client, resp := h.login(t, "jack", validPassword)
	expectStatus(t, resp, 200)
	expectStatus(t, h.request(t, "POST", "/auth/password", map[string]string{
		"current_password": validPassword,
		"new_password":     "SYNSECRET-NEWPW-9921-z",
	}, client), 204)

	// The audit table contains none of the secrets.
	rows, err := h.db.Query(`SELECT event, actor_id, target_type, target_id, result, request_id FROM audit_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var event, actor, result string
		var targetType, targetID, requestID *string
		if err := rows.Scan(&event, &actor, &targetType, &targetID, &result, &requestID); err != nil {
			t.Fatal(err)
		}
		for _, col := range []string{event, actor, result} {
			if strings.Contains(col, "SYNSECRET") {
				t.Fatalf("audit leak: %q", col)
			}
		}
	}

	// Logs contain none of the secrets; the setup token appears exactly once
	// and only in its dedicated event (D01).
	records, _ := h.logger.records.Load().([]slog.Record)
	tokenHits := 0
	for _, r := range records {
		var line strings.Builder
		line.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			line.WriteString("\x00" + a.Key + "\x01" + a.Value.String())
			return true
		})
		text := line.String()
		if strings.Contains(text, "SYNSECRET") {
			t.Fatalf("log leak: %s", text)
		}
		if strings.Contains(text, token) {
			tokenHits++
			if r.Message != bootstrap.SetupTokenEvent {
				t.Fatalf("setup token logged outside the dedicated event: %s", r.Message)
			}
		}
	}
	if tokenHits != 1 {
		t.Fatalf("setup token appeared %d times in logs, want exactly 1", tokenHits)
	}
	_ = io.Discard
}

// directAdmin creates and logs in an admin for system-audit queries.
func (h *auditHarness) directAdmin(t *testing.T) authClient {
	t.Helper()
	hash, err := auth.HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	ts := h.clock.Now().Format("2006-01-02T15:04:05.000000000Z")
	if _, err := h.db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at)
		VALUES('admin-internal','root','root','admin','active',0,?,?,?)`, hash, ts, ts); err != nil {
		t.Fatal(err)
	}
	c, resp := h.login(t, "root", validPassword)
	expectStatus(t, resp, 200)
	return c
}

func TestAuditCursorBindsEventFilter(t *testing.T) {
	h := newAuditHarness(t)
	h.user(t, "gina", false)
	for i := 0; i < 3; i++ {
		_, r := h.login(t, "gina", validPassword)
		expectStatus(t, r, 200)
	}
	admin := h.directAdmin(t)
	r := h.request(t, "GET", "/admin/audit?limit=1&event=auth.login.success", nil, admin)
	b := decodeBody(t, r)
	cursor := b["next_cursor"].(string)
	r = h.request(t, "GET", "/admin/audit?limit=1&event=auth.login.failure&cursor="+url.QueryEscape(cursor), nil, admin)
	status := r.StatusCode
	b = decodeBody(t, r)
	t.Logf("changed event with original cursor: status=%d body=%v", status, b)
	if status != 400 {
		t.Fatal("cursor not bound to event filter")
	}
}
