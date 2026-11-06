package redact

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "protocol", "fixtures", "redact", "*.json"))
	if len(files) == 0 {
		t.Fatal("no redact fixtures found")
	}
	n := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var cases []struct{ In, Out string }
		if err := json.Unmarshal(raw, &cases); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, c := range cases {
			n++
			if got := String(c.In); got != c.Out {
				t.Errorf("%s: String(%q) = %q, want %q", filepath.Base(f), c.In, got, c.Out)
			}
		}
	}
	t.Logf("%d redact cases", n)
}

func TestIdempotent(t *testing.T) {
	in := "token=abc123 Bearer abcdefgh12345 sk-ant-xxxxxxxxxxxx"
	once := String(in)
	if String(once) != once {
		t.Errorf("not idempotent: %q -> %q", once, String(once))
	}
}

func TestValue(t *testing.T) {
	in := map[string]any{
		"note":     "use sk-ant-abcdefghijkl now",
		"password": "plain",
		"nested":   map[string]any{"API_KEY": "zzz", "list": []any{"bt_run_abcdefgh12", 3, true, nil}},
		"n":        json.Number("12345678901234567890"),
		"token":    5, // non-string value of a sensitive key is walked, not blanked
	}
	want := map[string]any{
		"note":     "use sk-ant-[REDACTED] now",
		"password": "[REDACTED]",
		"nested":   map[string]any{"API_KEY": "[REDACTED]", "list": []any{"bt_run_[REDACTED]", 3, true, nil}},
		"n":        json.Number("12345678901234567890"),
		"token":    5,
	}
	got := Value(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
	if in["password"] != "plain" {
		t.Error("input mutated")
	}
	if Value(nil) != nil || Value(1.5) != 1.5 {
		t.Error("primitives")
	}
}

func TestDepthCap(t *testing.T) {
	var v any = "leaf"
	for i := 0; i < 60; i++ {
		v = map[string]any{"a": v}
	}
	out := Value(v)
	depth := 0
	for {
		m, ok := out.(map[string]any)
		if !ok {
			break
		}
		out = m["a"]
		depth++
	}
	if out != "[REDACTED]" || depth != 51 {
		t.Errorf("depth=%d tail=%v", depth, out)
	}
}

func TestHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewHandler(slog.NewJSONHandler(&buf, nil)))
	log.With("secret", "abc").Info("hello bt_adm_abcdefghijk", "tok", "Bearer abcdefgh1234", "token", "x", slog.Group("g", "password", "p"))
	s := buf.String()
	for _, leak := range []string{"abcdefghijk", "abcdefgh1234", `"abc"`, `"x"`, `"p"`} {
		if strings.Contains(s, leak) {
			t.Errorf("leaked %s in %s", leak, s)
		}
	}
}
