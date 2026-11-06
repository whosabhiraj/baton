package spool_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/spool"
	"github.com/whosabhiraj/baton/internal/store"
)

func decodeNum(b []byte) any {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		panic(err)
	}
	return v
}

func matches(got, want any) bool {
	if w, ok := want.(string); ok && w == "<any>" {
		return got != nil
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			if !matches(g[k], wv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !matches(g[i], w[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(got, want)
}

type warnings struct{ codes []string }

func (w *warnings) fn(_, code, _ string, _ int64) { w.codes = append(w.codes, code) }

func setup(t *testing.T, dir, runID, agent string) (*store.Store, *store.Run) {
	t.Helper()
	s, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := s.Get(runID)
	if r == nil {
		r, _, err = s.Create(store.CreateOpts{RunID: runID, AgentID: agent})
		if err != nil {
			t.Fatal(err)
		}
	}
	return s, r
}

func TestOutboxFixtures(t *testing.T) {
	cases, _ := filepath.Glob("../../protocol/fixtures/outbox/*.case")
	if len(cases) == 0 {
		t.Fatal("no outbox fixtures")
	}
	for _, c := range cases {
		t.Run(filepath.Base(c), func(t *testing.T) {
			raw, _ := os.ReadFile(filepath.Join(c, "expect.json"))
			var fx struct {
				Run struct {
					RunID   string `json:"run_id"`
					AgentID string `json:"agent_id"`
				} `json:"run"`
				Stored      []json.RawMessage `json:"stored"`
				Warnings    []string          `json:"warnings"`
				FinalOffset int64             `json:"final_offset"`
			}
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			_, r := setup(t, t.TempDir(), fx.Run.RunID, fx.Run.AgentID)
			data, _ := os.ReadFile(filepath.Join(c, "outbox.jsonl"))
			if err := os.WriteFile(filepath.Join(r.Dir(), paths.OutboxFile), data, 0o644); err != nil {
				t.Fatal(err)
			}
			w := &warnings{}
			tl := spool.NewOutboxTailer(r, w.fn)
			if err := tl.Poll(); err != nil {
				t.Fatal(err)
			}
			if tl.Offset() != fx.FinalOffset {
				t.Fatalf("offset = %d, want %d", tl.Offset(), fx.FinalOffset)
			}
			if !reflect.DeepEqual(append([]string{}, w.codes...), append([]string{}, fx.Warnings...)) {
				t.Fatalf("warnings = %v, want %v", w.codes, fx.Warnings)
			}
			evs, _ := r.Events(0)
			if len(evs) != len(fx.Stored) {
				t.Fatalf("stored %d events, want %d", len(evs), len(fx.Stored))
			}
			for i, ev := range evs {
				line, _ := json.Marshal(ev)
				if !matches(decodeNum(line), decodeNum(fx.Stored[i])) {
					t.Fatalf("event %d\n got: %s\nwant: %s", i, line, fx.Stored[i])
				}
			}
			off, _ := os.ReadFile(filepath.Join(r.Dir(), paths.OutboxOffsetFile))
			if strings.TrimSpace(string(off)) != strconv.FormatInt(fx.FinalOffset, 10) && fx.FinalOffset != 0 {
				t.Fatalf("persisted offset %q", off)
			}
		})
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

func line(id string) string {
	return fmt.Sprintf(`{"v":1,"run_id":"r1","agent_id":"a1","type":"heartbeat","msg_id":%q}`+"\n", id)
}

func TestPartialLineCompletedLater(t *testing.T) {
	_, r := setup(t, t.TempDir(), "r1", "a1")
	p := filepath.Join(r.Dir(), paths.OutboxFile)
	l := line("m1")
	appendFile(t, p, l+l[:20])
	tl := spool.NewOutboxTailer(r, nil)
	tl.Poll()
	if r.LastSeq() != 1 || tl.Offset() != int64(len(l)) {
		t.Fatalf("seq=%d off=%d", r.LastSeq(), tl.Offset())
	}
	appendFile(t, p, l[20:])
	tl.Poll()
	// second line has same msg_id as first -> dedupe, no new event
	if r.LastSeq() != 1 || tl.Offset() != int64(2*len(l)) {
		t.Fatalf("seq=%d off=%d", r.LastSeq(), tl.Offset())
	}
}

func TestOversizedLineSkipped(t *testing.T) {
	_, r := setup(t, t.TempDir(), "r1", "a1")
	p := filepath.Join(r.Dir(), paths.OutboxFile)
	big := `{"v":1,"run_id":"r1","agent_id":"a1","type":"progress","payload":{"percent":1,"note":"` +
		strings.Repeat("x", spool.DefaultMaxLine+10) + `"},"msg_id":"big"}` + "\n"
	appendFile(t, p, line("a")+big+line("b"))
	w := &warnings{}
	tl := spool.NewOutboxTailer(r, w.fn)
	tl.Poll()
	if r.LastSeq() != 2 {
		t.Fatalf("seq = %d", r.LastSeq())
	}
	if len(w.codes) != 1 || w.codes[0] != spool.WarnLineTooLong {
		t.Fatalf("warnings %v", w.codes)
	}
	fi, _ := os.Stat(p)
	if tl.Offset() != fi.Size() {
		t.Fatal("offset must advance past the big line")
	}
}

func TestTruncatedFileResets(t *testing.T) {
	_, r := setup(t, t.TempDir(), "r1", "a1")
	p := filepath.Join(r.Dir(), paths.OutboxFile)
	appendFile(t, p, line("a")+line("b"))
	w := &warnings{}
	tl := spool.NewOutboxTailer(r, w.fn)
	tl.Poll()
	// replace with a shorter file containing a new message
	os.WriteFile(p, []byte(line("c")), 0o644)
	tl.Poll()
	if r.LastSeq() != 3 {
		t.Fatalf("seq = %d", r.LastSeq())
	}
	if len(w.codes) != 1 || w.codes[0] != spool.WarnTruncated {
		t.Fatalf("warnings %v", w.codes)
	}
}

func TestOffsetPersistedAndDedupeCrashSafe(t *testing.T) {
	dir := t.TempDir()
	s, r := setup(t, dir, "r1", "a1")
	p := filepath.Join(r.Dir(), paths.OutboxFile)
	appendFile(t, p, line("a")+line("b")+`{"v":1,"run_id":"r1","agent_id":"a1","type":"heartbeat"}`+"\n")
	spool.NewOutboxTailer(r, nil).Poll()
	if r.LastSeq() != 3 {
		t.Fatal(r.LastSeq())
	}
	s.Close()

	// "crash before offset commit": delete the offset file; everything is re-read, nothing duplicated
	os.Remove(filepath.Join(r.Dir(), paths.OutboxOffsetFile))
	s2, r2 := setup(t, dir, "r1", "a1")
	_ = s2
	tl := spool.NewOutboxTailer(r2, nil)
	tl.Poll()
	if r2.LastSeq() != 3 {
		t.Fatalf("duplicates after re-read: seq=%d", r2.LastSeq())
	}
	// with the offset present nothing is re-read
	tl2 := spool.NewOutboxTailer(r2, nil)
	n := 0
	tl2.Handle = func([]byte, int64) error { n++; return nil }
	tl2.Poll()
	if n != 0 {
		t.Fatalf("re-read %d lines despite committed offset", n)
	}
}

func TestManagerDrainAllAndPoll(t *testing.T) {
	dir := t.TempDir()
	st, r := setup(t, dir, "r1", "a1")
	appendFile(t, filepath.Join(r.Dir(), paths.OutboxFile), line("a"))
	w := &warnings{}
	m := spool.NewManager(st, 20_000_000, slogDiscard(), w.fn)
	m.DrainAll()
	if r.LastSeq() != 1 {
		t.Fatal(r.LastSeq())
	}
	m.Start()
	defer m.Stop()
	appendFile(t, filepath.Join(r.Dir(), paths.OutboxFile), line("b"))
	waitFor(t, func() bool { return r.LastSeq() == 2 })
	_ = proto.Version
}
