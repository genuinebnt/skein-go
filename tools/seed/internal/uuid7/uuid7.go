// Package uuid7 builds UUIDv7 identifiers (RFC 9562) deterministically, from
// a timestamp and a random stream, so that seed data has realistic,
// time-ordered IDs that are the same on every run.
//
// Layout: 48 bits of Unix milliseconds, the version nibble 7, 12 random
// bits, the variant bits 10, and 62 random bits. IDs created at the same
// millisecond order by their random bits, as real v7 generators without a
// counter do.
//
// Porting notes: Rust's `uuid` crate formats the same 16 bytes identically
// (lowercase, hyphenated 8-4-4-4-12).
package uuid7

import (
	"encoding/hex"
	"time"

	"github.com/genuinebnt/skein-go/tools/seed/internal/prng"
)

// New returns the canonical string form of a UUIDv7 for time t.
func New(t time.Time, r *prng.Rand) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (40 - 8*i))
	}
	x, y := r.Uint64(), r.Uint64()
	b[6] = 0x70 | byte(x&0x0F)
	b[7] = byte(x >> 8)
	b[8] = 0x80 | byte((x>>16)&0x3F)
	for i := 9; i < 16; i++ {
		b[i] = byte(y >> (8 * (i - 9)))
	}
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}
