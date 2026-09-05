package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func testKey(t *testing.T) *MasterKey {
	t.Helper()
	raw := bytes.Repeat([]byte{0x42}, 32)
	mk, err := NewMasterKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return mk
}

func validAAD() AAD {
	return AAD{
		ItemID:         "0198f0a2-1111-7222-8333-444455556666",
		Scope:          "personal",
		SubjectID:      "user-1",
		PayloadVersion: PayloadVersion,
		Revision:       1,
	}
}

func TestMasterKeyRejectsWrongLength(t *testing.T) {
	for _, n := range []int{0, 1, 16, 31, 33, 64} {
		if _, err := NewMasterKey(make([]byte, n)); err == nil {
			t.Errorf("key of %d bytes accepted", n)
		}
	}
	if _, err := NewMasterKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Errorf("32-byte key rejected: %v", err)
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey(t)
	plaintext := []byte(`{"name":"Bank","password":"s3cr3t-🔒"}`)
	blob, err := key.EncryptEncoded(plaintext, validAAD())
	if err != nil {
		t.Fatal(err)
	}
	got, err := key.DecryptEncoded(blob, validAAD())
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(plaintext, got) {
		t.Fatalf("round trip mismatch")
	}
	// The stored blob must not contain the plaintext.
	if bytes.Contains(blob, []byte("s3cr3t")) {
		t.Fatal("plaintext leaked into ciphertext blob")
	}
}

func TestWrongKeyFails(t *testing.T) {
	key := testKey(t)
	otherRaw := bytes.Repeat([]byte{0x24}, 32)
	other, _ := NewMasterKey(otherRaw)

	blob, err := key.EncryptEncoded([]byte("secret payload"), validAAD())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.DecryptEncoded(blob, validAAD()); err == nil {
		t.Fatal("decryption under a different key succeeded")
	}
}

func TestTamperAnyAADFieldFails(t *testing.T) {
	key := testKey(t)
	plaintext := []byte(`{"v":1}`)

	mutations := map[string]func(*AAD){
		"item id":         func(a *AAD) { a.ItemID = "0198f0a2-1111-7222-8333-444455557777" },
		"scope":           func(a *AAD) { a.Scope = "shared" },
		"subject id":      func(a *AAD) { a.SubjectID = "user-2" },
		"payload version": func(a *AAD) { a.PayloadVersion = 2 },
		"revision":        func(a *AAD) { a.Revision = 2 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			blob, err := key.EncryptEncoded(plaintext, validAAD())
			if err != nil {
				t.Fatal(err)
			}
			aad := validAAD()
			mutate(&aad)
			if _, err := key.DecryptEncoded(blob, aad); err == nil {
				t.Fatalf("tampered %s accepted", name)
			}
		})
	}
}

func TestTamperCiphertextAndNonceFails(t *testing.T) {
	key := testKey(t)
	blob, err := key.EncryptEncoded([]byte("secret payload"), validAAD())
	if err != nil {
		t.Fatal(err)
	}

	// Flip one byte inside the ciphertext region (last byte).
	tampered := bytes.Clone(blob)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := key.DecryptEncoded(tampered, validAAD()); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}

	// Flip one byte inside the nonce region (bytes 2..26).
	tampered = bytes.Clone(blob)
	tampered[2] ^= 0x01
	if _, err := key.DecryptEncoded(tampered, validAAD()); err == nil {
		t.Fatal("tampered nonce accepted")
	}

	// Corrupt the version header.
	tampered = bytes.Clone(blob)
	tampered[0] = 0x09
	if _, err := key.DecryptEncoded(tampered, validAAD()); err == nil {
		t.Fatal("corrupt version accepted")
	}
}

func TestNoncesAreUniquePerEncryption(t *testing.T) {
	key := testKey(t)
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		p, err := key.Encrypt([]byte("same plaintext"), validAAD())
		if err != nil {
			t.Fatal(err)
		}
		s := string(p.Nonce[:])
		if _, dup := seen[s]; dup {
			t.Fatal("nonce reuse detected")
		}
		seen[s] = struct{}{}
	}
}

func TestHistoryVersionsKeepOwnRevisionAAD(t *testing.T) {
	key := testKey(t)
	// Simulate history: each revision re-encrypts the payload with its own AAD.
	rev1, err := key.EncryptEncoded([]byte("revision one body"), AAD{ItemID: "item-1", Scope: "personal", SubjectID: "user-1", PayloadVersion: 1, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	rev2, err := key.EncryptEncoded([]byte("revision two body"), AAD{ItemID: "item-1", Scope: "personal", SubjectID: "user-1", PayloadVersion: 1, Revision: 2})
	if err != nil {
		t.Fatal(err)
	}

	got1, err := key.DecryptEncoded(rev1, AAD{ItemID: "item-1", Scope: "personal", SubjectID: "user-1", PayloadVersion: 1, Revision: 1})
	if err != nil || string(got1) != "revision one body" {
		t.Fatalf("history revision 1: %v %q", err, got1)
	}
	got2, err := key.DecryptEncoded(rev2, AAD{ItemID: "item-1", Scope: "personal", SubjectID: "user-1", PayloadVersion: 1, Revision: 2})
	if err != nil || string(got2) != "revision two body" {
		t.Fatalf("history revision 2: %v %q", err, got2)
	}
	// Cross-revision AAD must fail (a history blob cannot be presented as the
	// current revision or vice versa).
	if _, err := key.DecryptEncoded(rev1, AAD{ItemID: "item-1", Scope: "personal", SubjectID: "user-1", PayloadVersion: 1, Revision: 2}); err == nil {
		t.Fatal("revision 1 blob decrypted with revision 2 AAD")
	}
}

func TestAADValidation(t *testing.T) {
	if err := ValidateAAD(validAAD()); err != nil {
		t.Fatalf("valid AAD rejected: %v", err)
	}
	bad := validAAD()
	bad.ItemID = ""
	if err := ValidateAAD(bad); err == nil {
		t.Fatal("empty item id accepted")
	}
	bad = validAAD()
	bad.Scope = "team"
	if err := ValidateAAD(bad); err == nil {
		t.Fatal("unknown scope accepted")
	}
	bad = validAAD()
	bad.SubjectID = ""
	if err := ValidateAAD(bad); err == nil {
		t.Fatal("empty subject accepted")
	}
	bad = validAAD()
	bad.Revision = 0
	if err := ValidateAAD(bad); err == nil {
		t.Fatal("revision 0 accepted")
	}
}

func TestDecodeRejectsShortAndForeignBlobs(t *testing.T) {
	if _, err := DecodeEncryptedPayload([]byte{1, 2, 3}); err == nil {
		t.Fatal("short blob accepted")
	}
	long := bytes.Repeat([]byte{0}, 100)
	if _, err := DecodeEncryptedPayload(long); err == nil {
		t.Fatal("foreign blob accepted")
	}
	if !errors.Is(nil, nil) {
		t.Fatal("sanity")
	}
}

func TestMarkerDetectsKeyChange(t *testing.T) {
	keyA := testKey(t)
	rawB := bytes.Repeat([]byte{0x99}, 32)
	keyB, _ := NewMasterKey(rawB)

	marker := keyA.Marker()
	if !keyA.VerifyMarker(marker) {
		t.Fatal("marker does not verify under its own key")
	}
	if keyB.VerifyMarker(marker) {
		t.Fatal("marker verified under a different key")
	}
	// Marker is deterministic.
	if keyA.Marker() != marker {
		t.Fatal("marker not deterministic")
	}
}
