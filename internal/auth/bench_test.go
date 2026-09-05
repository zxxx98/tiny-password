package auth

import "testing"

func BenchmarkPasswordHash(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := HashPassword("benchmark-password-42"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPasswordVerify(b *testing.B) {
	hash, err := HashPassword("benchmark-password-42")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, err := VerifyPassword("benchmark-password-42", hash)
		if err != nil || !ok {
			b.Fatalf("verify: ok=%v err=%v", ok, err)
		}
	}
}
