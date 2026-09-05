// Package ident generates resource identifiers.
package ident

import (
	"crypto/rand"

	"sync"
	"time"
)

// UUIDv7 returns a time-ordered RFC 9562 UUID (48-bit Unix millisecond
// timestamp + 74 bits of CSPRNG randomness). Monotonicity within the same
// millisecond is guaranteed by a process-wide counter in the rand_a field.
var (
	monoMu   sync.Mutex
	monoLast int64
	monoSeq  uint16
)

// NewUUIDv7 returns a new UUIDv7 string in canonical form.
func NewUUIDv7() string {
	var u [16]byte

	monoMu.Lock()
	ms := time.Now().UnixMilli()
	if ms <= monoLast {
		ms = monoLast
		monoSeq++
	} else {
		monoLast = ms
		monoSeq = 0
	}
	seq := monoSeq
	monoMu.Unlock()

	// unix_ts_ms: 48 bits big-endian (RFC 9562).
	u[0] = byte(ms >> 40)
	u[1] = byte(ms >> 32)
	u[2] = byte(ms >> 24)
	u[3] = byte(ms >> 16)
	u[4] = byte(ms >> 8)
	u[5] = byte(ms)
	// rand_a: 12 bits, version 7 in the high nibble.
	u[6] = 0x70 | byte(seq>>8&0x0f)
	u[7] = byte(seq)
	// rand_b: variant 10xx in the high two bits.
	if _, err := rand.Read(u[8:16]); err != nil {
		panic("ident: crypto/rand unavailable: " + err.Error())
	}
	u[8] = (u[8] & 0x3f) | 0x80

	return formatUUID(u)
}

func formatUUID(u [16]byte) string {
	const hexdigits = "0123456789abcdef"
	var out [36]byte
	pos := 0
	for i, b := range u {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[pos] = '-'
			pos++
		}
		out[pos] = hexdigits[b>>4]
		out[pos+1] = hexdigits[b&0x0f]
		pos += 2
	}
	return string(out[:])
}
