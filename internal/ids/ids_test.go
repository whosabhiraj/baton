package ids

import (
	"encoding/base64"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestFormats(t *testing.T) {
	cases := []struct {
		name, got, re string
	}{
		{"RunID", RunID(), `^r_[0-9A-HJKMNP-TV-Z]{26}$`},
		{"UUID", UUID(), `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`},
		{"QID", QID(), `^q_[0-9a-z]{6}$`},
		{"RunToken", RunToken(), `^bt_run_[A-Za-z0-9_-]{43}$`},
		{"AdminToken", AdminToken(), `^bt_adm_[A-Za-z0-9_-]{43}$`},
	}
	for _, c := range cases {
		if !regexp.MustCompile(c.re).MatchString(c.got) {
			t.Errorf("%s = %q does not match %s", c.name, c.got, c.re)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(RunToken(), "bt_run_"))
	if err != nil || len(raw) != 32 {
		t.Errorf("token payload: len=%d err=%v", len(raw), err)
	}
}

func TestUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		for _, s := range []string{RunID(), UUID(), RunToken()} {
			if seen[s] {
				t.Fatalf("duplicate %q", s)
			}
			seen[s] = true
		}
	}
}

func TestULIDKnownEncoding(t *testing.T) {
	// time 0 -> first 10 chars are zeros.
	if got := ulid(0)[:10]; got != "0000000000" {
		t.Errorf("ulid(0) prefix = %q", got)
	}
	// 48-bit max time -> "7ZZZZZZZZZ" (ULID spec max timestamp).
	if got := ulid(1<<48 - 1)[:10]; got != "7ZZZZZZZZZ" {
		t.Errorf("ulid(max) prefix = %q", got)
	}
	// 1469918176385 ms -> 01ARYZ6S41 (reference value from the ULID spec).
	if got := ulid(1469918176385)[:10]; got != "01ARYZ6S41" {
		t.Errorf("ulid(1469918176385) prefix = %q", got)
	}
}

func TestULIDSortable(t *testing.T) {
	var list []string
	for ms := uint64(1_700_000_000_000); ms < 1_700_000_000_000+200; ms += 7 {
		list = append(list, ulid(ms))
	}
	if !sort.StringsAreSorted(list) {
		t.Error("ulids not sorted by time")
	}
}

func TestTokenHash(t *testing.T) {
	tok := RunToken()
	h := HashToken(tok)
	if len(h) != 64 {
		t.Fatalf("hash len %d", len(h))
	}
	if HashToken("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Error("sha256 mismatch")
	}
	if !TokenMatches(h, tok) {
		t.Error("should match")
	}
	if TokenMatches(h, tok+"x") || TokenMatches("", tok) || TokenMatches(h[:10], tok) {
		t.Error("should not match")
	}
}
