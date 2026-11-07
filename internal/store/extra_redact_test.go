package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/paths"
)

func TestExtraFieldsRedacted(t *testing.T) {
	s := openStore(t, t.TempDir())
	r := newRun(t, s)
	e := hb("x1")
	e.Extra = map[string]json.RawMessage{"note": json.RawMessage(`{"k":"bt_adm_abcdefghijk","n":3}`), "plain": json.RawMessage(`"ok"`)}
	if _, _, perr := r.IngestEnvelope(e, "cli"); perr != nil {
		t.Fatal(perr)
	}
	b, err := os.ReadFile(filepath.Join(r.Dir(), paths.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "abcdefghijk") || !strings.Contains(string(b), "bt_adm_[REDACTED]") || !strings.Contains(string(b), `"plain":"ok"`) {
		t.Fatalf("extra not redacted: %s", b)
	}
}
