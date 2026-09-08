package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/ident"
	"github.com/tiny-password/tiny-password/internal/vault"
	"github.com/tiny-password/tiny-password/tests/fixtures"
)

// TestScaleBaseline creates the T30 release-scale fixture: 20 member
// accounts and 10,000 encrypted personal items, distributed evenly so the
// result is representative of a multi-user instance. It intentionally uses
// the synthetic seed path rather than 10,000 HTTP requests; the test checks
// the resulting database shape while the wrapper script records process RSS.
func TestScaleBaseline(t *testing.T) {
	if testing.Short() || raceDetector {
		t.Skip("scale baseline skipped with -short or under -race")
	}

	const (
		members        = 20
		totalItems     = 10000
		itemsPerMember = totalItems / members
	)
	if totalItems%members != 0 {
		t.Fatal("scale fixture must distribute items evenly")
	}

	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	// Reuse the already-valid synthetic admin hash so this data-load baseline
	// does not include 20 Argon2 allocations. Argon2 cost is measured by the
	// dedicated auth benchmark instead.
	var passwordHash string
	if err := h.db.QueryRow("SELECT password_hash FROM users WHERE username_norm='admin'").Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	memberIDs := make([]string, 0, members)
	for i := 0; i < members; i++ {
		name := fmt.Sprintf("scale-member-%02d", i)
		id := ident.NewUUIDv7()
		at := h.clock.Now().UTC().Format(time.RFC3339Nano)
		if _, err := h.db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at)
			VALUES(?,?,?,'member','active',0,?,?,?)`, id, name, name, passwordHash, at, at); err != nil {
			t.Fatalf("insert member %d: %v", i, err)
		}
		memberIDs = append(memberIDs, h.lookupUserID(t, name))
	}

	for i, memberID := range memberIDs {
		if _, err := fixtures.SeedLoginItems(
			context.Background(), h.db.DB, mustMasterKey(t), memberID,
			itemsPerMember, itemsPerMember/2, fmt.Sprintf("scale-needle-%02d", i),
		); err != nil {
			t.Fatalf("seed member %d: %v", i, err)
		}
	}

	var userCount, itemCount int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM users WHERE role='member'").Scan(&userCount); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow("SELECT COUNT(*) FROM vault_items").Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if userCount != members || itemCount != totalItems {
		t.Fatalf("scale fixture counts: members=%d items=%d, want members=%d items=%d", userCount, itemCount, members, totalItems)
	}
	var encryptedLoginCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM vault_items
		WHERE vault_scope='personal' AND item_type='login' AND payload_version=1
		AND length(nonce)=24 AND length(ciphertext)>16`).Scan(&encryptedLoginCount); err != nil {
		t.Fatal(err)
	}
	if encryptedLoginCount != totalItems {
		t.Fatalf("valid encrypted login rows=%d, want %d", encryptedLoginCount, totalItems)
	}
	var sampleID, sampleOwner string
	var sampleVersion, sampleRevision int
	var sampleNonce, sampleCiphertext []byte
	if err := h.db.QueryRow(`SELECT id, owner_user_id, payload_version, revision, nonce, ciphertext
		FROM vault_items WHERE vault_scope='personal' AND item_type='login' LIMIT 1`).Scan(
		&sampleID, &sampleOwner, &sampleVersion, &sampleRevision, &sampleNonce, &sampleCiphertext); err != nil {
		t.Fatal(err)
	}
	plaintext, err := mustMasterKey(t).DecryptColumns(uint16(sampleVersion), sampleNonce, sampleCiphertext,
		vault.AADFor(sampleID, "personal", sampleOwner, "", uint16(sampleVersion), uint64(sampleRevision)))
	if err != nil {
		t.Fatalf("decrypt scale sample: %v", err)
	}
	if !json.Valid(plaintext) {
		t.Fatal("decrypted scale sample is not valid JSON")
	}
	for i, memberID := range memberIDs {
		var count int
		if err := h.db.QueryRow("SELECT COUNT(*) FROM vault_items WHERE owner_user_id=?", memberID).Scan(&count); err != nil {
			t.Fatalf("count member %d: %v", i, err)
		}
		if count != itemsPerMember {
			t.Fatalf("member %d items=%d, want %d", i, count, itemsPerMember)
		}
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	procStatusKB := func(field string) int64 {
		data, err := os.ReadFile("/proc/self/status")
		if os.IsNotExist(err) {
			return -1
		}
		if err != nil {
			t.Fatalf("read process status: %v", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] != field+":" {
				continue
			}
			value, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				t.Fatalf("parse %s: %v", field, err)
			}
			return value
		}
		return -1
	}
	logFileSize := func(path string) int64 {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		return info.Size()
	}
	t.Logf("scale baseline members=%d admin=1 items=%d items_per_member=%d heap_alloc_mb=%.2f heap_sys_mb=%.2f heap_delta_mb=%.2f rss_after_gc_kb=%d hwm_kb=%d db_bytes=%d wal_bytes=%d shm_bytes=%d",
		members, itemCount, itemsPerMember,
		float64(after.HeapAlloc)/(1024*1024),
		float64(after.HeapSys)/(1024*1024),
		float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/(1024*1024),
		procStatusKB("VmRSS"), procStatusKB("VmHWM"),
		logFileSize(h.db.Path()), logFileSize(h.db.Path()+"-wal"), logFileSize(h.db.Path()+"-shm"))
}
