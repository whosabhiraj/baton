package hub_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
	"github.com/whosabhiraj/baton/internal/store"
)

type fakeLauncher struct {
	mu       sync.Mutex
	startErr error
	started  []string
	killed   []string
}

func (f *fakeLauncher) Start(res runs.SpawnResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, res.RunID)
	return f.startErr
}
func (f *fakeLauncher) Kill(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, id)
	return nil
}

func TestUsageAndAccountTotals(t *testing.T) {
	o, _ := p2opts(t)
	h := start(t, o)
	_, _, err := h.Store().Create(newRun("r1", "a1"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = h.Store().Create(newRun("r2", "a1"))
	for _, p := range []map[string]any{
		{"input_tokens": 10, "output_tokens": 5},
		{"input_tokens": 100, "output_tokens": 50, "cost_usd": 0.5, "model": "m"}, // latest wins
	} {
		if _, e := h.Store().Get("r1").AppendHub("usage", p); e != nil {
			t.Fatal(e)
		}
	}
	_, _ = h.Store().Get("r2").AppendHub("usage", map[string]any{"input_tokens": 1, "output_tokens": 2, "cache_read_tokens": 3, "cost_usd": 0.25})
	if _, e := h.Store().Get("r2").AppendHub("usage", map[string]any{"input_tokens": -1, "output_tokens": 2}); e == nil {
		t.Fatal("negative usage accepted")
	}

	_, m := call(t, h, "GET", "/api/state", "tok", "")
	if err := proto.ValidateJSON("api.state", m); err != nil {
		t.Fatal(err)
	}
	if c := m["conductor"].(map[string]any); c["state"] != "off" {
		t.Fatal(c)
	}
	var withUsage int
	for _, r := range m["runs"].([]any) {
		if r.(map[string]any)["usage"] != nil {
			withUsage++
		}
	}
	if withUsage != 2 {
		t.Fatal(m["runs"])
	}
	u := m["accounts"].([]any)[0].(map[string]any)["usage"].(map[string]any)
	if u["input_tokens"].(interface{ String() string }).String() != "101" ||
		u["output_tokens"].(interface{ String() string }).String() != "52" ||
		u["cache_read_tokens"].(interface{ String() string }).String() != "3" ||
		u["cache_write_tokens"].(interface{ String() string }).String() != "0" ||
		u["cost_usd"].(interface{ String() string }).String() != "0.75" {
		t.Fatal(u)
	}
}

func TestConductorStateAndNotice(t *testing.T) {
	o, _ := p2opts(t)
	h := start(t, o)
	ch, stop := openStream(t, h)
	defer stop()
	waitLine(t, ch, "event: hello")
	h.SetConductor(proto.ConductorState{State: "thinking", Account: "a1", Model: "opus"})
	waitLine(t, ch, `"type":"conductor"`)
	_, m := call(t, h, "GET", "/api/state", "tok", "")
	c := m["conductor"].(map[string]any)
	if c["state"] != "thinking" || c["account"] != "a1" || c["since"] == "" {
		t.Fatal(c)
	}
}

func TestApprovals(t *testing.T) {
	o, _ := p2opts(t)
	var mu sync.Mutex
	var decided []approvals.Approval
	o.OnApproval = func(a approvals.Approval) { mu.Lock(); decided = append(decided, a); mu.Unlock() }
	h := start(t, o)
	ch, stop := openStream(t, h)
	defer stop()
	waitLine(t, ch, "event: hello")

	ap, err := h.RequestApproval("merge", "r1", "Merge x", map[string]any{"branch": "b"}, "conductor")
	if err != nil {
		t.Fatal(err)
	}
	waitLine(t, ch, `"type":"approval"`)
	_, m := call(t, h, "GET", "/api/state", "tok", "")
	if err := proto.ValidateJSON("api.state", m); err != nil || len(m["approvals"].([]any)) != 1 {
		t.Fatal(err, m["approvals"])
	}
	if st, _ := call(t, h, "GET", "/api/approvals?status=bogus", "tok", ""); st != 400 {
		t.Fatal(st)
	}
	if st, r := call(t, h, "POST", "/api/approvals/nope/approve", "tok", ""); st != 404 || r["code"] != "NOT_FOUND" {
		t.Fatal(st, r)
	}
	st, r := call(t, h, "POST", "/api/approvals/"+ap.ID+"/approve", "tok", `{"note":"lgtm"}`)
	if st != 200 || r["status"] != "approved" || r["decided_by"] != "human" || r["note"] != "lgtm" {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "POST", "/api/approvals/"+ap.ID+"/reject", "tok", ""); st != 409 || r["code"] != "ALREADY_DECIDED" {
		t.Fatal(st, r)
	}
	waitFor(t, "OnApproval", func() bool { mu.Lock(); defer mu.Unlock(); return len(decided) == 1 })
	_, m = call(t, h, "GET", "/api/state", "tok", "")
	if len(m["approvals"].([]any)) != 0 {
		t.Fatal("decided approval still pending")
	}
	h.Close(context.Background())

	// persisted: a new hub on the same data dir sees it
	h2 := start(t, o)
	got, err := approvals.Open(h2.DataDir)
	if err != nil || len(got.List("")) != 1 || got.List("")[0].Status != "approved" {
		t.Fatal(err, got.List(""))
	}
	_, a := call(t, h2, "GET", "/api/audit?limit=0", "tok", "")
	var acts []string
	for _, e := range a["entries"].([]any) {
		acts = append(acts, e.(map[string]any)["action"].(string))
	}
	if g := strings.Join(acts, ","); !strings.Contains(g, "approval.request") || !strings.Contains(g, "approval.approve") {
		t.Fatal(g)
	}
}

func TestKillRoute(t *testing.T) {
	o, _ := p2opts(t)
	o.Dev = true
	h := start(t, o)
	run := devRun(t, h, `{"role":"x"}`)
	rid := run["run_id"].(string)
	if st, r := call(t, h, "POST", "/api/runs/"+rid+"/kill", "tok", ""); st != 409 || r["code"] != "NOT_SUPPORTED" {
		t.Fatal(st, r)
	}
	h.Close(context.Background())

	fl := &fakeLauncher{}
	o.Launcher = fl
	h = start(t, o)
	if st, r := call(t, h, "POST", "/api/runs/nope/kill", "tok", ""); st != 404 {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "POST", "/api/runs/"+rid+"/kill", "tok", ""); st != 200 || r["ok"] != true || len(fl.killed) != 1 {
		t.Fatal(st, r)
	}
	h.Store().Get(rid).AppendHub("killed", map[string]any{"detail": "x"})
	if st, r := call(t, h, "POST", "/api/runs/"+rid+"/kill", "tok", ""); st != 410 || r["code"] != "RUN_ENDED" {
		t.Fatal(st, r)
	}
}

func TestLaunchFailure(t *testing.T) {
	o, _ := p2opts(t)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := gitx.Init(repo); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLauncher{}
	o.Repo, o.Launcher = repo, fl
	h := start(t, o)
	body := `{"account":"a1","role":"implementer","task":"t"}`
	if st, r := call(t, h, "POST", "/api/spawn", "tok", body); st != 200 || len(fl.started) != 1 {
		t.Fatal(st, r)
	}
	// a1 allows one concurrent run: end the first one, then fail the launch of the next
	first := fl.started[0]
	h.Store().Get(first).AppendHub("exit", map[string]any{"code": 0, "signal": nil, "diff_stat": nil, "duration_s": 1})
	if st, _ := call(t, h, "POST", "/api/spawn", "tok", `{"account":"a1","role":"implementer","task":"t","dry_run":true}`); st != 200 || len(fl.started) != 1 {
		t.Fatal("dry run started an agent")
	}
	fl.startErr = errors.New("boom")
	st, r := call(t, h, "POST", "/api/spawn", "tok", body)
	if st != 500 || r["code"] != "LAUNCH_FAILED" || r["run_id"] == nil {
		t.Fatal(st, r)
	}
	run := h.Store().Get(r["run_id"].(string))
	if run == nil || !run.Ended() {
		t.Fatal("failed launch left the run alive")
	}
	evs, _ := run.Events(0)
	if last := evs[len(evs)-1]; last.Type != "exit" || last.Payload["code"].(interface{ String() string }).String() != "-1" {
		t.Fatal(last)
	}
}

func newRun(id, account string) store.CreateOpts {
	return store.CreateOpts{RunID: id, AgentID: "ag-" + id, AccountID: account}
}
