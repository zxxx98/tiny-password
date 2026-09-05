package auth

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/migrations"
)

const testPassword = "correct horse battery 42"

func authFixture(t *testing.T) (*Service, *Service, string) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = sqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	now := timestamp(time.Now())
	if _, err = db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at) VALUES('alice','alice','alice','member','active',0,?,?,?)`, hash, now, now); err != nil {
		t.Fatal(err)
	}
	options := Options{Limits: Limits{Username: 100, Source: 100, Global: 100}}
	first, err := NewService(db.DB, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewService(db.DB, options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := first.Login(context.Background(), "alice", testPassword, "test")
	if err != nil {
		t.Fatal(err)
	}
	return first, second, result.Token
}
func pauseVerification(s *Service, entered chan<- struct{}, release <-chan struct{}) {
	s.verify = func(password, hash string) (bool, error) {
		ok, err := VerifyPassword(password, hash)
		entered <- struct{}{}
		<-release
		return ok, err
	}
}
func waitVerification(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("verification did not start")
	}
}
func TestAuthLoginCannotRacePasswordChange(t *testing.T) {
	stale, current, token := authFixture(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	pauseVerification(stale, entered, release)
	done := make(chan error, 1)
	go func() { _, err := stale.Login(context.Background(), "alice", testPassword, "login"); done <- err }()
	waitVerification(t, entered)
	rotated, err := current.ChangePassword(context.Background(), token, testPassword, "new correct battery 55", "change")
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale login: %v", err)
	}
	if _, err = current.Authenticate(context.Background(), token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old credential: %v", err)
	}
	if _, err = current.Authenticate(context.Background(), rotated.Token); err != nil {
		t.Fatal(err)
	}
}
func TestPasswordConcurrentChangesOnlyOneCommits(t *testing.T) {
	first, second, token := authFixture(t)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	pauseVerification(first, entered, release)
	pauseVerification(second, entered, release)
	done := make(chan error, 2)
	go func() {
		_, err := first.ChangePassword(context.Background(), token, testPassword, "first password update 55", "first")
		done <- err
	}()
	go func() {
		_, err := second.ChangePassword(context.Background(), token, testPassword, "second password update 66", "second")
		done <- err
	}()
	waitVerification(t, entered)
	waitVerification(t, entered)
	close(release)
	successes, denied := 0, 0
	for i := 0; i < 2; i++ {
		err := <-done
		if err == nil {
			successes++
		} else if errors.Is(err, ErrUnauthorized) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || denied != 1 {
		t.Fatalf("success=%d denied=%d", successes, denied)
	}
	var active int
	if err := first.db.QueryRow("SELECT count(*) FROM sessions WHERE revoked_at IS NULL").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("live sessions=%d", active)
	}
}
func TestPasswordRevocationDuringVerificationCannotIssueSession(t *testing.T) {
	pending, current, token := authFixture(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	pauseVerification(pending, entered, release)
	done := make(chan error, 1)
	go func() {
		_, err := pending.ChangePassword(context.Background(), token, testPassword, "new correct battery 55", "change")
		done <- err
	}()
	waitVerification(t, entered)
	err := current.Logout(context.Background(), token)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked request committed: %v", err)
	}
	if err := current.Touch(context.Background(), token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked session revived: %v", err)
	}
}
func TestPasswordRotationFailureRollsBackHashAndRevocations(t *testing.T) {
	first, _, token := authFixture(t)
	if _, err := first.db.Exec(`CREATE TRIGGER fail_new_session BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ChangePassword(context.Background(), token, testPassword, "new correct battery 55", "change"); err == nil {
		t.Fatal("injected failure ignored")
	}
	if _, err := first.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("old session lost: %v", err)
	}
	var hash string
	if err := first.db.QueryRow("SELECT password_hash FROM users WHERE id='alice'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword(testPassword, hash); err != nil || !ok {
		t.Fatalf("password changed despite rollback: %v", err)
	}
}
func TestAuthRateLimitConcurrentAdmission(t *testing.T) {
	first, second, _ := authFixture(t)
	first.limits = Limits{Username: 5, Source: 100, Global: 100}
	second.limits = first.limits
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := first
			if i%2 == 0 {
				s = second
			}
			results <- s.reserveAttempt(context.Background(), "limited", "source")
		}(i)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrRateLimited) {
			t.Fatal(err)
		}
	}
	if accepted != 5 {
		t.Fatalf("concurrent admitted=%d want=5", accepted)
	}
}

func TestAuthLoginUsesLatestIdlePreference(t *testing.T) {
	pending, current, token := authFixture(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	pauseVerification(pending, entered, release)
	type outcome struct {
		result *LoginResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := pending.Login(context.Background(), "alice", testPassword, "login")
		done <- outcome{r, err}
	}()
	waitVerification(t, entered)
	err := current.SetIdleTimeout(context.Background(), token, 5)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	created, err := time.Parse(time.RFC3339Nano, result.result.Principal.Session.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, result.result.Principal.Session.IdleExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if result.result.Principal.IdleTimeoutMinutes != 5 || expires.Sub(created) != 5*time.Minute {
		t.Fatalf("stale login preference: %+v", result.result.Principal)
	}
}
