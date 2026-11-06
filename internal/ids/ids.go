// Package ids generates run ids, UUIDs, tokens and question ids, and hashes tokens.
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return b
}

// ulid encodes a 48-bit millisecond time plus 80 random bits as 26 Crockford base32 chars
// (128 bits, big-endian, 5 bits per char, first char holds only the top 3 bits).
func ulid(ms uint64) string {
	var v [16]byte
	for i := 0; i < 6; i++ {
		v[i] = byte(ms >> (8 * (5 - i)))
	}
	copy(v[6:], randBytes(10))
	out := make([]byte, 26)
	// Walk bits from the least significant end.
	for i := 25; i >= 0; i-- {
		bitPos := (25 - i) * 5 // bit offset from the LSB
		var x uint
		for b := 0; b < 5; b++ {
			p := bitPos + b
			if p >= 128 {
				break
			}
			byteIdx := 15 - p/8
			x |= uint(v[byteIdx]>>(uint(p)%8)&1) << uint(b)
		}
		out[i] = crockford[x]
	}
	return string(out)
}

// RunID returns "r_" + a 26-character ULID (time-sortable).
func RunID() string { return "r_" + ulid(uint64(time.Now().UnixMilli())) }

// UUID returns a random (version 4) UUID string.
func UUID() string {
	b := randBytes(16)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// RunToken returns "bt_run_" + base64url of 32 random bytes.
func RunToken() string { return "bt_run_" + base64.RawURLEncoding.EncodeToString(randBytes(32)) }

// AdminToken returns "bt_adm_" + base64url of 32 random bytes.
func AdminToken() string { return "bt_adm_" + base64.RawURLEncoding.EncodeToString(randBytes(32)) }

// QID returns a question id: "q_" + 6 lowercase base36 characters.
func QID() string {
	const alpha = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 6)
	for i := range out {
		// 252 = 7*36 is the largest multiple of 36 below 256: reject above it to avoid bias.
		for {
			b := randBytes(1)[0]
			if b < 252 {
				out[i] = alpha[int(b)%36]
				break
			}
		}
	}
	return "q_" + string(out)
}

// HashToken returns the lowercase hex SHA-256 of tok.
func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// TokenMatches reports, in constant time, whether tok hashes to hashHex.
func TokenMatches(hashHex, tok string) bool {
	return subtle.ConstantTimeCompare([]byte(hashHex), []byte(HashToken(tok))) == 1
}
