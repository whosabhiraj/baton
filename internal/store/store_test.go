package store_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/store"
)

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newRun(t *testing.T, s *store.Store) *store.Run {
	t.Helper()
	r, _, err := s.Create(store.CreateOpts{RunID: "r_TEST", AgentID: "a1", Plan: []proto.PlanStep{
		{ID: "s1", Title: "T1", Acceptance: "A1"}, {ID: "s2", Title: "T2", Acceptance: "A2"}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hb(id string) proto.Envelope {
	return proto.Envelope{V: 1, RunID: "r_TEST", AgentID: "a1", Type: "heartbeat", MsgID: id}
}

func TestConcurrentSeq(t *testing.T) {
	s := openStore(t, t.TempDir())
	r := newRun(t, s)
	var wg sync.WaitGroup
	seqs := make(chan int64, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seq, dup, err := r.IngestEnvelope(hb(fmt.Sprintf("m%d", i)), proto.SrcCLI)
			if err != nil || dup {
				t.Errorf("ingest %d: dup=%v err=%v", i, dup, err)
			}
			seqs <- seq
		}(i)
	}
	wg.Wait()
	close(seqs)
	seen := map[int64]bool{}
	for q := range seqs {
		seen[q] = true
	}
	for i := int64(1); i <= 50; i++ {
		if !seen[i] {
			t.Fatalf("seq %d missing", i)
		}
	}
	evs, _ := r.Events(0)
	if len(evs) != 50 {
		t.Fatalf("events = %d", len(evs))
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, ev.Seq)
		}
	}
}

func TestDuplicateMsgID(t *testing.T) {
	s := openStore(t, t.TempDir())
	r := newRun(t, s)
	a, dup, _ := r.IngestEnvelope(hb("x"), proto.SrcCLI)
	b, dup2, _ := r.IngestEnvelope(hb("x"), proto.SrcSpool)
	if a != 1 || dup || b != 1 || !dup2 {
		t.Fatalf("a=%d dup=%v b=%d dup2=%v", a, dup, b, dup2)
	}
	if evs, _ := r.Events(0); len(evs) != 1 {
		t.Fatalf("stored %d", len(evs))
	}
}

func TestTornLastLineRecovered(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(dir, store.Options{})
	r := newRun(t, s)
	r.IngestEnvelope(hb("a"), proto.SrcCLI)
	r.IngestEnvelope(hb("b"), proto.SrcCLI)
	s.Close()
	p := filepath.Join(paths.RunDir(dir, "r_TEST"), paths.EventsFile)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"v":1,"run_id":"r_TEST","agent_id":"a1","seq":3,"ts":"2026`)
	f.Close()

	s2 := openStore(t, dir)
	r2 := s2.Get("r_TEST")
	if r2.LastSeq() != 2 {
		t.Fatalf("lastSeq = %d", r2.LastSeq())
	}
	seq, dup, err := r2.IngestEnvelope(hb("c"), proto.SrcCLI)
	if err != nil || dup || seq != 3 {
		t.Fatalf("seq=%d dup=%v err=%v", seq, dup, err)
	}
	b, _ := os.ReadFile(p)
	if bytes.Count(b, []byte("\n")) != 3 {
		t.Fatalf("file should have 3 lines:\n%s", b)
	}
	// also: a complete-but-unparsable last line is dropped
	s2.Close()
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("garbage\n")
	f.Close()
	s3 := openStore(t, dir)
	if s3.Get("r_TEST").LastSeq() != 3 {
		t.Fatal("garbage last line should be dropped")
	}
}

func TestInboxRepair(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(dir, store.Options{})
	r := newRun(t, s)
	n, err := r.Deliver("message", map[string]any{"from": "human", "text": "hi"})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	s.Close()
	// simulate crash: file for n=2 written, to_agent event missing
	msg := proto.InboxMessage{V: 1, N: 2, TS: "2026-11-06T14:00:00.000Z", Type: "pause", Payload: map[string]any{}}
	os.WriteFile(filepath.Join(paths.RunDir(dir, "r_TEST"), "inbox", paths.InboxFileName(2)), proto.MarshalLine(msg), 0o644)

	s2 := openStore(t, dir)
	r2 := s2.Get("r_TEST")
	evs, _ := r2.Events(0)
	var ns []any
	for _, ev := range evs {
		if ev.Type == "to_agent" {
			ns = append(ns, ev.Payload["n"])
		}
	}
	if len(ns) != 2 || fmt.Sprint(ns[0]) != "1" || fmt.Sprint(ns[1]) != "2" {
		t.Fatalf("to_agent events: %v", ns)
	}
	if n, _ := r2.Deliver("resume", nil); n != 3 {
		t.Fatalf("next n = %d", n)
	}
	msgs, _ := r2.Inbox(1)
	if len(msgs) != 2 || msgs[0].N != 2 {
		t.Fatalf("inbox after 1: %+v", msgs)
	}
	// repair is idempotent
	s2.Close()
	s3 := openStore(t, dir)
	evs, _ = s3.Get("r_TEST").Events(0)
	if len(evs) != 3 {
		t.Fatalf("events after second reopen = %d", len(evs))
	}
}

func TestRestartContinuesSeq(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(dir, store.Options{})
	r := newRun(t, s)
	r.IngestEnvelope(hb("a"), proto.SrcCLI)
	r.IngestEnvelope(hb("b"), proto.SrcCLI)
	s.Close()
	s2 := openStore(t, dir)
	r2 := s2.Get("r_TEST")
	if _, dup, _ := r2.IngestEnvelope(hb("a"), proto.SrcCLI); !dup {
		t.Fatal("dedupe must survive restart")
	}
	seq, _, _ := r2.IngestEnvelope(hb("c"), proto.SrcCLI)
	if seq != 3 {
		t.Fatalf("seq = %d", seq)
	}
}

func TestDoneRevokesTokenAndPersists(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(dir, store.Options{})
	r, tok, _ := s.Create(store.CreateOpts{RunID: "r_TEST", AgentID: "a1"})
	if !r.CheckToken(tok) || s.RunByToken(tok) != r {
		t.Fatal("token should work")
	}
	_, _, err := r.IngestEnvelope(proto.Envelope{V: 1, RunID: "r_TEST", AgentID: "a1", Type: "done",
		Payload: map[string]any{"status": "success", "summary": "ok"}, MsgID: "d"}, proto.SrcCLI)
	if err != nil {
		t.Fatal(err)
	}
	if r.CheckToken(tok) || !r.TokenRevoked() {
		t.Fatal("token should be revoked")
	}
	if _, _, err := r.IngestEnvelope(hb("late"), proto.SrcCLI); err == nil || err.Code != proto.CodeRunEnded {
		t.Fatalf("err = %v", err)
	}
	s.Close()
	raw, _ := os.ReadFile(filepath.Join(paths.RunDir(dir, "r_TEST"), paths.RunRecordFile))
	if bytes.Contains(raw, []byte(tok)) {
		t.Fatal("plaintext token in run.json")
	}
	var rec proto.RunRecord
	json.Unmarshal(raw, &rec)
	if rec.Status != proto.StatusDone || !rec.TokenRevoked || rec.EndedAt == "" {
		t.Fatalf("record: %+v", rec)
	}
}

func TestStatusDerivation(t *testing.T) {
	s := openStore(t, t.TempDir())
	r := newRun(t, s)
	send := func(typ string, payload map[string]any) {
		t.Helper()
		_, _, err := r.IngestEnvelope(proto.Envelope{V: 1, RunID: "r_TEST", AgentID: "a1", Type: typ, Payload: payload}, proto.SrcCLI)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	status := func() proto.RunStatus { return r.Summary().Status }
	if status() != proto.StatusStarting {
		t.Fatal(status())
	}
	send("hello", map[string]any{"brief_ack": true})
	if status() != proto.StatusRunning {
		t.Fatal(status())
	}
	send("step_start", map[string]any{"step_id": "s1"})
	if r.Summary().StepStates["s1"] != "active" || r.Summary().StepStates["s2"] != "pending" {
		t.Fatalf("%v", r.Summary().StepStates)
	}
	send("question", map[string]any{"qid": "q_1", "text": "?", "blocking": true})
	if status() != proto.StatusWaiting || len(r.Summary().OpenQuestions) != 1 {
		t.Fatal(status())
	}
	send("heartbeat", nil)
	if status() != proto.StatusWaiting {
		t.Fatal("heartbeat must not leave waiting")
	}
	r.Deliver("answer", map[string]any{"qid": "q_1", "text": "yes"})
	if status() != proto.StatusRunning || len(r.Summary().OpenQuestions) != 0 {
		t.Fatalf("%s %v", status(), r.Summary().OpenQuestions)
	}
	send("step_blocked", map[string]any{"step_id": "s1", "reason": "x"})
	if status() != proto.StatusBlocked || r.Summary().StepStates["s1"] != "blocked" {
		t.Fatal(status())
	}
	send("step_done", map[string]any{"step_id": "s1", "summary": "ok"})
	if status() != proto.StatusRunning || r.Summary().StepStates["s1"] != "done" {
		t.Fatal(status())
	}
	r.AppendHub("paused", map[string]any{})
	send("progress", map[string]any{"percent": 5, "note": "n"})
	if status() != proto.StatusPaused {
		t.Fatal("progress must not unpause")
	}
	r.AppendHub("resumed", map[string]any{})
	if status() != proto.StatusRunning {
		t.Fatal(status())
	}
	send("done", map[string]any{"status": "failed", "summary": "x"})
	if status() != proto.StatusFailed {
		t.Fatal(status())
	}
}

func TestStalledEmitAndClear(t *testing.T) {
	s := openStore(t, t.TempDir())
	r := newRun(t, s)
	r.IngestEnvelope(hb("a"), proto.SrcCLI)
	timeout := 100 * time.Millisecond
	if r.CheckStalled(time.Now(), timeout) {
		t.Fatal("not yet")
	}
	later := time.Now().Add(time.Second)
	if !r.CheckStalled(later, timeout) {
		t.Fatal("should stall")
	}
	if r.CheckStalled(later, timeout) {
		t.Fatal("must not emit twice")
	}
	if r.Summary().Status != proto.StatusStalled || r.Ended() {
		t.Fatal("status should be stalled, run not ended")
	}
	evs, _ := r.Events(1)
	if len(evs) != 1 || evs[0].Type != "stalled" || evs[0].Src != proto.SrcHub || fmt.Sprint(evs[0].Payload["timeout_s"]) != "0.1" {
		t.Fatalf("%+v", evs)
	}
	if err := proto.ValidateJSON("envelope.stored", evs[0]); err != nil {
		t.Fatal(err)
	}
	r.IngestEnvelope(hb("b"), proto.SrcCLI)
	if r.Summary().Status != proto.StatusRunning {
		t.Fatalf("status = %s", r.Summary().Status)
	}
	if r.CheckStalled(time.Now(), timeout) {
		t.Fatal("fresh activity")
	}
}

// Agent output that stores no event (thinking deltas, tool chatter) still counts as alive.
func TestTouchKeepsStallAway(t *testing.T) {
	s := openStore(t, t.TempDir())
	r := newRun(t, s)
	r.IngestEnvelope(hb("a"), proto.SrcCLI)
	timeout := 200 * time.Millisecond
	time.Sleep(timeout + 50*time.Millisecond)
	r.Touch()
	if r.CheckStalled(time.Now(), timeout) {
		t.Fatal("touched just now: must not stall")
	}
	if r.Summary().Status == proto.StatusStalled {
		t.Fatal("status stalled after touch")
	}
	if !r.CheckStalled(time.Now().Add(time.Second), timeout) {
		t.Fatal("silence after the touch should still stall")
	}
}

// ---- fixtures ----

func normalise(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = normalise(val)
		}
	case []any:
		for i := range x {
			x[i] = normalise(x[i])
		}
	}
	return v
}

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

func TestIngestFixtures(t *testing.T) {
	files, _ := filepath.Glob("../../protocol/fixtures/ingest/*.json")
	if len(files) == 0 {
		t.Fatal("no ingest fixtures found")
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, _ := os.ReadFile(file)
			var fx struct {
				Run struct {
					RunID   string           `json:"run_id"`
					AgentID string           `json:"agent_id"`
					Plan    []proto.PlanStep `json:"plan"`
				} `json:"run"`
				Incoming []json.RawMessage `json:"incoming"`
				Expect   []json.RawMessage `json:"expect"`
			}
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			s := openStore(t, t.TempDir())
			r, _, err := s.Create(store.CreateOpts{RunID: fx.Run.RunID, AgentID: fx.Run.AgentID, Plan: fx.Run.Plan})
			if err != nil {
				t.Fatal(err)
			}
			if len(fx.Incoming) != len(fx.Expect) {
				t.Fatal("fixture shape")
			}
			for i, in := range fx.Incoming {
				exp := decodeNum(fx.Expect[i]).(map[string]any)
				seq, dup, perr := r.Ingest(in, proto.SrcCLI)
				if exp["ok"] == false {
					if perr == nil || perr.Code != exp["code"] {
						t.Fatalf("#%d: want code %v, got seq=%d err=%v", i, exp["code"], seq, perr)
					}
					continue
				}
				if perr != nil {
					t.Fatalf("#%d: unexpected error %v", i, perr)
				}
				if fmt.Sprint(exp["seq"]) != fmt.Sprint(seq) || exp["duplicate"] != dup {
					t.Fatalf("#%d: seq=%d dup=%v, want %v/%v", i, seq, dup, exp["seq"], exp["duplicate"])
				}
				if exp["stored"] == nil {
					continue
				}
				evs, _ := r.Events(seq - 1)
				if len(evs) == 0 {
					t.Fatalf("#%d: event not found", i)
				}
				line, _ := json.Marshal(evs[0])
				if err := proto.ValidateJSON("envelope.stored", decodeNum(line)); err != nil {
					t.Fatalf("#%d: stored event invalid: %v", i, err)
				}
				if !matches(normalise(decodeNum(line)), normalise(exp["stored"])) {
					t.Fatalf("#%d: stored mismatch\n got: %s\nwant: %v", i, line, exp["stored"])
				}
			}
		})
	}
}

func TestSetExtraPersists(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(t, s)
	if err := r.SetExtra("merged", true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetExtra("status", "x"); err == nil {
		t.Fatal("core field must be refused")
	}
	if err := r.SetExtra("note", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetExtra("note", nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(r.Dir(), paths.RunRecordFile))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["merged"] != true || m["note"] != nil || m["run_id"] != "r_TEST" {
		t.Fatalf("run.json = %s", b)
	}
	s.Close()
	s2 := openStore(t, dir)
	b2, _ := os.ReadFile(filepath.Join(s2.Get("r_TEST").Dir(), paths.RunRecordFile))
	if !bytes.Contains(b2, []byte(`"merged": true`)) {
		t.Fatalf("lost after reopen: %s", b2)
	}
}
