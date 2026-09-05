package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/webassets"
)

// testJSON writes a JSON response (httpapi's writer is unexported).
func testJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func testJSONError(w http.ResponseWriter, status int, code, message string) {
	testJSON(w, status, map[string]any{"code": code, "message": message, "request_id": "test"})
}

func newIdempotencyKeyMaterial() []byte {
	return bytes.Repeat([]byte{0x5A}, 32)
}

// fixedTimestamp matches the fixed-width UTC format the auth service writes,
// keeping lexicographic timestamp comparisons valid for test-inserted rows.
const fixedTimestamp = "2006-01-02T15:04:05.000000000Z"

// newIdempotentSetupHarness wires the setup endpoints with idempotency.
func newIdempotentSetupHarness(t *testing.T) *setupHarness {
	t.Helper()
	db := openMigratedDB(t)
	logger := &capturedHandler{}
	slogLogger := slog.New(logger)

	key, err := crypto.NewMasterKey(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := bootstrap.NewService(db, key, slogLogger)
	if err != nil {
		t.Fatalf("bootstrap service: %v", err)
	}
	authSvc, err := auth.NewService(db.DB, auth.Options{Limits: auth.Limits{Username: 1000, Source: 1000, Global: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	idem, err := idempotency.NewService(db.DB, idempotency.Options{MACKey: newIdempotencyKeyMaterial()})
	if err != nil {
		t.Fatal(err)
	}
	csrf := httpapi.NewPreAuthCSRF(false)
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		SPA:    webassets.SPAHandler(),
		Setup:  &httpapi.SetupDeps{Service: svc, CSRF: csrf, Logger: slogLogger, RateLimit: 100, Idempotency: idem},
		Auth:   &httpapi.AuthDeps{Service: authSvc, CSRF: csrf},
		Logger: slogLogger,
	}))
	t.Cleanup(srv.Close)
	return &setupHarness{db: db, server: srv, logger: logger}
}

// setupRequestWithKey performs the pre-auth dance with an optional
// Idempotency-Key and returns the response.
func setupRequestWithKey(t *testing.T, srvURL, token, username, password, idemKey string) *http.Response {
	t.Helper()
	client := &http.Client{}

	csrfResp, err := client.Post(srvURL+"/api/v1/csrf", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var csrfBody struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(csrfResp.Body).Decode(&csrfBody); err != nil {
		t.Fatalf("decode csrf response: %v", err)
	}
	cookies := csrfResp.Cookies()
	csrfResp.Body.Close()

	body, _ := json.Marshal(map[string]string{
		"token":    token,
		"username": username,
		"password": password,
	})
	req, err := http.NewRequest(http.MethodPost, srvURL+"/api/v1/setup/init", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srvURL)
	req.Header.Set("X-CSRF-Token", csrfBody.CSRFToken)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	out, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSetupIdempotentReplayAndConflict(t *testing.T) {
	h := newIdempotentSetupHarness(t)
	token := h.logger.tokenValue()
	const key = "setup-key-0000000001"

	resp := setupRequestWithKey(t, h.server.URL, token, "Admin", validPassword, key)
	first := decodeBody(t, resp)
	if resp.StatusCode != http.StatusOK || first["initialized"] != true {
		t.Fatalf("init failed: status=%d body=%v", resp.StatusCode, first)
	}

	// Identical retry: replayed from the idempotency record, not re-executed
	// (which would answer 409 SETUP_ALREADY_DONE).
	resp = setupRequestWithKey(t, h.server.URL, token, "Admin", validPassword, key)
	replay := decodeBody(t, resp)
	if resp.StatusCode != http.StatusOK || replay["username"] != first["username"] {
		t.Fatalf("replay: status=%d body=%v want=%v", resp.StatusCode, replay, first)
	}

	// Same key, different content: conflict.
	resp = setupRequestWithKey(t, h.server.URL, token, "Admin", "a-different-password-42", key)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || body["code"] != "IDEMPOTENCY_KEY_CONFLICT" {
		t.Fatalf("conflict: status=%d body=%v", resp.StatusCode, body)
	}

	// Without a key the closed entry point answers normally.
	resp = setupRequestWithKey(t, h.server.URL, token, "Admin", validPassword, "")
	body = decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || body["code"] != "SETUP_ALREADY_DONE" {
		t.Fatalf("plain retry: status=%d body=%v", resp.StatusCode, body)
	}
}

func TestSetupRejectsMalformedIdempotencyKey(t *testing.T) {
	h := newIdempotentSetupHarness(t)
	token := h.logger.tokenValue()

	resp := setupRequestWithKey(t, h.server.URL, token, "Admin", validPassword, "short")
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || body["code"] != "VALIDATION_ERROR" {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
}

// noteHarness mounts a synthetic create endpoint mirroring the production
// idempotency flow that T09 productionizes, so cross-user isolation,
// revocation-before-replay and concurrent single-effect are all exercised
// over real HTTP with a live session check.
type noteHarness struct {
	db     *sqlite.DB
	svc    *auth.Service
	server *httptest.Server
	clock  *authClock
}

func newNoteHarness(t *testing.T) *noteHarness {
	t.Helper()
	db := openMigratedDB(t)
	clock := &authClock{value: time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)}
	svc, err := auth.NewService(db.DB, auth.Options{Now: clock.Now, Limits: auth.Limits{Username: 1000, Source: 1000, Global: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	idem, err := idempotency.NewService(db.DB, idempotency.Options{MACKey: newIdempotencyKeyMaterial(), Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE test_notes (id TEXT PRIMARY KEY, owner TEXT NOT NULL, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/test/notes", httpapi.RequireSession(svc, false, createNoteHandler(db, idem)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &noteHarness{db: db, svc: svc, server: srv, clock: clock}
}

func createNoteHandler(db *sqlite.DB, idem *idempotency.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := httpapi.CurrentPrincipal(r.Context())
		var input struct {
			Value string `json:"value"`
		}
		if !decodeNoteBody(w, r, &input) {
			return
		}
		scope := httpapi.ScopeFor("test.notes.create", p.UserID)
		key, present, invalid := httpapi.IdempotencyKey(r)
		if invalid {
			testJSONError(w, 400, "VALIDATION_ERROR", "invalid idempotency key")
			return
		}
		if !present {
			testJSONError(w, 400, "VALIDATION_ERROR", "idempotency key required")
			return
		}
		fp := idem.Fingerprint(scope, input.Value)
		claim, outcome, err := idem.Claim(r.Context(), scope, key, fp)
		if err != nil {
			testJSONError(w, 500, "INTERNAL", "claim failed")
			return
		}
		switch outcome {
		case idempotency.OutcomeFresh:
			noteID, err := insertNote(r.Context(), db, claim, p.UserID, input.Value)
			if err != nil {
				claim.Release(r.Context())
				testJSONError(w, 500, "INTERNAL", "create failed")
				return
			}
			testJSON(w, http.StatusCreated, map[string]string{"id": noteID, "value": input.Value})
		case idempotency.OutcomeInFlight:
			testJSONError(w, 409, "CONFLICT", "an identical request is still in progress")
		case idempotency.OutcomeConflict:
			testJSONError(w, 409, "IDEMPOTENCY_KEY_CONFLICT", "key used with different content")
		default:
			resourceID, err := idem.ReplayResourceID(r.Context(), scope, key, fp)
			if err != nil {
				testJSONError(w, 409, "CONFLICT", "idempotency record unavailable")
				return
			}
			// Authorization was re-verified by RequireSession; render the
			// stored opaque resource id if it still exists.
			var value string
			err = db.QueryRowContext(r.Context(),
				`SELECT value FROM test_notes WHERE id=? AND owner=?`, resourceID, p.UserID).Scan(&value)
			if err != nil {
				testJSONError(w, 409, "CONFLICT", "the original resource no longer exists")
				return
			}
			testJSON(w, http.StatusCreated, map[string]string{"id": resourceID, "value": value})
		}
	})
}

// insertNote runs the business change and the idempotency completion inside
// one transaction, like production create handlers must.
func insertNote(ctx context.Context, db *sqlite.DB, claim *idempotency.Claim, owner, value string) (string, error) {
	noteID := fmt.Sprintf("note-%s", hex.EncodeToString([]byte(owner))[:6]) + fmt.Sprintf("-%d", time.Now().UnixNano())
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO test_notes (id, owner, value) VALUES (?, ?, ?)`, noteID, owner, value); err != nil {
		return "", err
	}
	if err := claim.Complete(ctx, tx, noteID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return noteID, nil
}

func decodeNoteBody(w http.ResponseWriter, r *http.Request, target any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		testJSONError(w, 400, "VALIDATION_ERROR", "invalid request body")
		return false
	}
	return true
}

func (h *noteHarness) client(t *testing.T, name string) authClient {
	t.Helper()
	hash, err := auth.HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	now := h.clock.Now().Format(fixedTimestamp)
	_, err = h.db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at)
		VALUES(?,?,?,'member','active',0,?,?,?)`, name, name, name, hash, now, now)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	_, err = h.db.Exec(`INSERT INTO sessions(id,public_id,user_id,created_at,absolute_expires_at,idle_expires_at)
		VALUES(?,?,?,?,?,?)`, hex.EncodeToString(sum[:]), "pub-"+name, name,
		now, h.clock.Now().Add(24*time.Hour).Format(fixedTimestamp), h.clock.Now().Add(15*time.Minute).Format(fixedTimestamp))
	if err != nil {
		t.Fatal(err)
	}
	return authClient{
		cookie: &http.Cookie{Name: "tiny_password_session", Value: token},
		csrf:   auth.CSRFToken(token),
	}
}

func (h *noteHarness) post(t *testing.T, client authClient, key, value string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"value": value})
	req, err := http.NewRequest("POST", h.server.URL+"/api/v1/test/notes", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-CSRF-Token", client.csrf)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if client.cookie != nil {
		req.AddCookie(client.cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestIdempotentCreateCrossUserAndRevocation(t *testing.T) {
	h := newNoteHarness(t)
	alice := h.client(t, "alice")
	bob := h.client(t, "bob")
	const key = "note-key-00000000001"

	// Alice creates with the key.
	resp := h.post(t, alice, key, "v1")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice create: %d", resp.StatusCode)
	}
	first := decodeBody(t, resp)

	// Alice's retry replays the same resource.
	resp = h.post(t, alice, key, "v1")
	replay := decodeBody(t, resp)
	if resp.StatusCode != http.StatusCreated || replay["id"] != first["id"] {
		t.Fatalf("replay mismatch: %v vs %v", replay, first)
	}

	// Same key from Bob never hits Alice's record: it creates his own note.
	resp = h.post(t, bob, key, "v1")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob create: %d", resp.StatusCode)
	}
	bobs := decodeBody(t, resp)
	if bobs["id"] == first["id"] {
		t.Fatal("bob must not receive alice's resource")
	}

	// Same key, different content: conflict for the same actor.
	resp = h.post(t, alice, key, "different")
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || body["code"] != "IDEMPOTENCY_KEY_CONFLICT" {
		t.Fatalf("conflict: %d %v", resp.StatusCode, body)
	}

	// After the actor's session is revoked, replay is impossible.
	if err := h.svc.Logout(context.Background(), alice.cookie.Value); err != nil {
		t.Fatal(err)
	}
	resp = h.post(t, alice, key, "v1")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session replay must fail, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Exactly two notes exist (alice's and bob's); no duplicates.
	if got := countNotes(h.db); got != 2 {
		t.Fatalf("notes=%d, want 2", got)
	}
}

func TestIdempotentCreateConcurrentExactlyOne(t *testing.T) {
	h := newNoteHarness(t)
	carol := h.client(t, "carol")
	const key = "note-key-00000000002"

	var wg sync.WaitGroup
	type result struct {
		status int
		id     string
	}
	results := make(chan result, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := h.post(t, carol, key, "concurrent")
			defer resp.Body.Close()
			r := result{status: resp.StatusCode}
			if resp.StatusCode == http.StatusCreated {
				var m map[string]string
				if err := json.NewDecoder(resp.Body).Decode(&m); err == nil {
					r.id = m["id"]
				}
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)

	created := map[string]bool{}
	for r := range results {
		switch r.status {
		case http.StatusCreated:
			// Either the winner or a latecomer replaying the completed claim.
			if r.id == "" {
				t.Fatal("created response without id")
			}
			created[r.id] = true
		case http.StatusConflict:
			// A loser that arrived while the winner was still pending.
		default:
			t.Fatalf("unexpected status %d", r.status)
		}
	}
	// The single-effect invariant holds regardless of interleaving; the
	// in-flight path itself is covered deterministically at service level.
	if len(created) != 1 {
		t.Fatalf("exactly one note must be created, got %v", created)
	}

	// After settlement a retry replays the single result.
	resp := h.post(t, carol, key, "concurrent")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post-settlement replay: %d", resp.StatusCode)
	}
	final := decodeBody(t, resp)
	for id := range created {
		if final["id"] != id {
			t.Fatalf("replay id %v != created %v", final["id"], id)
		}
	}
	if got := countNotes(h.db); got != 1 {
		t.Fatalf("notes=%d, want 1", got)
	}
}

func countNotes(db *sqlite.DB) int {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM test_notes`).Scan(&n); err != nil {
		return -1
	}
	return n
}
