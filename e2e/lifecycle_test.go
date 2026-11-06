package e2e

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/hub"
)

const lifeToken = "bt_adm_test_lifecycle"

func startHub(t *testing.T, dataDir string) *hub.Hub {
	t.Helper()
	// ctx is the hub lifetime (cancelling it shuts the hub down), so it must outlive the test.
	h, err := hub.Start(context.Background(), hub.Options{
		DataDir:    dataDir,
		Port:       0,
		Dev:        true,
		AdminToken: lifeToken,
		SpoolPoll:  100 * time.Millisecond,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("hub.Start: %v", err)
	}
	return h
}

func closeHub(h *hub.Hub) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = h.Close(ctx)
}

// Phase 1 acceptance: full agent lifecycle through the real binary and an in-process hub,
// including `ask --blocking` answered via the API and hub-down spooling with recovery.
func TestLifecycle(t *testing.T) {
	requireBin(t)
	dataDir := t.TempDir()
	h := startHub(t, dataDir)
	t.Cleanup(func() { closeHub(h) }) // reads the current h; Close is idempotent

	plan := []map[string]any{
		{"id": "s1", "title": "Write the thing", "acceptance": "node thing.js prints ok"},
		{"id": "s2", "title": "Test the thing", "acceptance": "npm test passes"},
	}
	reg := registerDev(t, h.URL, h.AdminToken, map[string]any{"role": "implementer", "kind": "generic", "plan": plan})
	env := fastEnv(reg.Env)
	runID := reg.RunID
	if _, err := os.Stat(filepath.Join(reg.RunDir, "run.json")); err != nil {
		t.Fatalf("run.json missing: %v", err)
	}
	adm := func(method, path string, body any) map[string]any {
		return api(t, h.URL, h.AdminToken, method, path, body)
	}

	ok := func(args ...string) {
		t.Helper()
		r := baton(t, env, args, "")
		if r.Code != 0 || !strings.HasPrefix(r.Out, "ok #") {
			t.Fatalf("baton %v: exit %d out=%q err=%q", args, r.Code, r.Out, r.Err)
		}
	}

	// --- hub up: report normally -------------------------------------------------------------
	ok("hello")
	ok("plan", "--confirm")
	ok("step", "s1", "start", "--note", "starting")
	ok("progress", "30", "scaffolded")

	// blocking ask: answer it through the API once the question shows up
	asking := batonAsync(env, []string{"ask", "Which db?", "--options", "sqlite,postgres", "--blocking", "--timeout", "20"}, 40*time.Second)
	q := until(t, 10*time.Second, "question event", func() (event, bool) {
		for _, e := range events(t, h.URL, h.AdminToken, runID) {
			if e.Type == "question" {
				return e, true
			}
		}
		return event{}, false
	})
	if q.Payload["blocking"] != true {
		t.Fatalf("question not blocking: %v", q.Payload)
	}
	if !reflect.DeepEqual(q.Payload["options"], []any{"sqlite", "postgres"}) {
		t.Fatalf("options = %v", q.Payload["options"])
	}
	if s := runSummary(t, h.URL, h.AdminToken, runID); s["status"] != "waiting" {
		t.Fatalf("status = %v, want waiting", s["status"])
	}
	// an unrelated message first: ask must not consume it
	if m := adm("POST", "/api/runs/"+runID+"/message", map[string]any{"text": "fyi: main moved", "from": "conductor"}); m["ok"] != true {
		t.Fatalf("message: %v", m)
	}
	if m := adm("POST", "/api/runs/"+runID+"/answer", map[string]any{"qid": q.Payload["qid"], "text": "sqlite"}); m["ok"] != true {
		t.Fatalf("answer: %v", m)
	}
	answered := waitRes(t, asking, 30*time.Second)
	if answered.Code != 0 || answered.Out != "sqlite" {
		t.Fatalf("ask result: %+v", answered)
	}

	inbox := baton(t, env, []string{"inbox"}, "")
	if inbox.Code != 0 || !strings.Contains(inbox.Out, "fyi: main moved") || strings.Contains(inbox.Out, "sqlite") {
		t.Fatalf("inbox: %+v", inbox)
	}
	if r := baton(t, env, []string{"inbox"}, ""); r.Out != "inbox empty" {
		t.Fatalf("second inbox = %q", r.Out)
	}

	ok("step", "s1", "done", "--summary", "thing works", "--evidence", "node thing.js")

	// --- hub down: the agent keeps working, CLI spools ---------------------------------------
	closeHub(h)
	spooled := func(args ...string) {
		t.Helper()
		r := baton(t, env, args, "")
		if r.Code != 0 || !strings.Contains(r.Out, "spooled") {
			t.Fatalf("baton %v: want spooled, got %+v", args, r)
		}
	}
	spooled("step", "s2", "start")
	spooled("artifact", "commit", "abc1234", "--note", "tests added")
	spooled("progress", "90", "tests green")
	// a hand-written spool line (Layer B), no msg_id
	hand, _ := json.Marshal(map[string]any{
		"v": 1, "run_id": runID, "agent_id": env["BATON_AGENT_ID"], "type": "progress",
		"payload": map[string]any{"percent": 95, "note": "by hand"},
	})
	appendFile(t, filepath.Join(reg.RunDir, "outbox.jsonl"), string(hand)+"\n")

	// --- hub restarts: recovers the outbox, no duplicates ------------------------------------
	h = startHub(t, dataDir)
	env["BATON_URL"] = h.URL // new random port
	evs := events(t, h.URL, h.AdminToken, runID)
	var notes []any
	spoolArtifact := false
	for _, e := range evs {
		if e.Type == "progress" {
			notes = append(notes, e.Payload["note"])
		}
		if e.Type == "artifact" && e.Src == "spool" {
			spoolArtifact = true
		}
	}
	if !reflect.DeepEqual(notes, []any{"scaffolded", "tests green", "by hand"}) {
		t.Fatalf("progress notes after recovery = %v", notes)
	}
	if !spoolArtifact {
		t.Fatal("no artifact event with src=spool")
	}

	ok("step", "s2", "done", "--summary", "tests pass", "--evidence", "npm test")
	ok("done", "--status", "success", "--summary", "thing written and tested")

	// restart again: nothing re-ingested twice
	closeHub(h)
	h = startHub(t, dataDir)
	evs = events(t, h.URL, h.AdminToken, runID)

	seen := map[string]bool{}
	for i, e := range evs {
		if e.Seq != i+1 {
			t.Fatalf("seq at index %d = %d: want 1..n with no gaps", i, e.Seq)
		}
		if seen[e.MsgID] {
			t.Fatalf("duplicate msg_id %s", e.MsgID)
		}
		seen[e.MsgID] = true
	}
	var types []string
	for _, e := range evs {
		if e.Src != "hub" && e.Type != "heartbeat" {
			types = append(types, e.Type)
		}
	}
	wantTypes := []string{
		"hello", "plan", "step_start", "progress", "question", "step_done",
		"step_start", "artifact", "progress", "progress", "step_done", "done",
	}
	if !reflect.DeepEqual(types, wantTypes) {
		t.Fatalf("types = %v\nwant    %v", types, wantTypes)
	}
	var planEv *event
	toAgentAnswer := false
	for i, e := range evs {
		if e.Type == "plan" && planEv == nil {
			planEv = &evs[i]
		}
		if e.Type == "to_agent" && e.Payload["type"] == "answer" {
			toAgentAnswer = true
		}
	}
	wantPlan := roundTrip(t, plan)
	if !reflect.DeepEqual(planEv.Payload["steps"], wantPlan) {
		t.Fatalf("plan --confirm did not fill in the conductor plan: %v", planEv.Payload["steps"])
	}
	if !toAgentAnswer {
		t.Fatal("no to_agent answer event")
	}

	final := runSummary(t, h.URL, h.AdminToken, runID)
	if final["status"] != "done" {
		t.Fatalf("final status = %v", final["status"])
	}
	if !reflect.DeepEqual(final["step_states"], map[string]any{"s1": "done", "s2": "done"}) {
		t.Fatalf("step_states = %v", final["step_states"])
	}

	// token revoked after done: CLI still exits 0
	env["BATON_URL"] = h.URL
	late := baton(t, env, []string{"progress", "100", "after done"}, "")
	if late.Code != 0 || !strings.Contains(late.Out+late.Err, "rejected") {
		t.Fatalf("late progress: %+v", late)
	}
	raw, err := os.ReadFile(filepath.Join(reg.RunDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var runJSON map[string]any
	if err := json.Unmarshal(raw, &runJSON); err != nil {
		t.Fatal(err)
	}
	if runJSON["token_revoked"] != true {
		t.Fatalf("token_revoked = %v", runJSON["token_revoked"])
	}
	if strings.Contains(string(raw), env["BATON_TOKEN"]) {
		t.Fatal("plaintext token stored in run.json")
	}
}

// roundTrip normalises a Go value to its generic JSON form (map[string]any / []any).
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
