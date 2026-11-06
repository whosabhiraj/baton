package runs_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
	"github.com/whosabhiraj/baton/internal/store"
)

type fixture struct {
	t    *testing.T
	repo string
	data string
	st   *store.Store
	cfg  config.Config
	m    *runs.Manager
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(context.Background(), dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func newFixture(t *testing.T, cfg config.Config) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "init")
	data := t.TempDir()
	st, err := store.Open(data, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{t: t, repo: repo, data: data, st: st, cfg: cfg}
	f.m = f.manager()
	return f
}

func (f *fixture) manager() *runs.Manager {
	m, err := runs.New(runs.Options{Repo: f.repo, DataDir: f.data, Store: f.st,
		Config: func() config.Config { return f.cfg }, HubURL: func() string { return "http://127.0.0.1:7777" }})
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func baseCfg() config.Config {
	return config.Config{
		Hub: config.Hub{Mode: config.ModePropose, MaxAgentsAuto: 2, AskTimeoutS: 900, PollIntervalS: 2, HeartbeatTimeoutS: 120},
		Accounts: []config.Account{
			{ID: "claude-b", Label: "Claude B", Kind: "claude", Enabled: true, MaxConcurrent: 2, Models: []string{"opus", "sonnet"}, Roles: []string{"impl", "review"}},
			{ID: "codex-1", Label: "Codex 1", Kind: "codex", Enabled: true, MaxConcurrent: 1},
			{ID: "off", Kind: "generic", Enabled: false, MaxConcurrent: 1},
		},
		Routing: map[string]string{"review": "claude-b"},
	}
}

func req(account string) runs.SpawnRequest {
	return runs.SpawnRequest{Account: account, Role: "impl", Task: "do the thing"}
}

func wantCode(t *testing.T, e *proto.Error, code string) {
	t.Helper()
	if e == nil || e.Code != code {
		t.Fatalf("want %s, got %v", code, e)
	}
}

func TestNewNotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	st, _ := store.Open(t.TempDir(), store.Options{})
	t.Cleanup(func() { st.Close() })
	non := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(non))
	_, err := runs.New(runs.Options{Repo: non, Store: st, Config: baseCfg})
	if e, ok := err.(*proto.Error); !ok || e.Code != proto.CodeNotARepo {
		t.Fatalf("err = %v", err)
	}
}

func TestSelectionErrorsInOrder(t *testing.T) {
	f := newFixture(t, baseCfg())
	ctx := context.Background()
	r := runs.SpawnRequest{Role: "impl", Task: "t"}
	_, e := f.m.Spawn(ctx, runs.SpawnRequest{Task: "t"})
	wantCode(t, e, proto.CodeInvalidPayload)
	_, e = f.m.Spawn(ctx, runs.SpawnRequest{Role: "impl"})
	wantCode(t, e, proto.CodeInvalidPayload)
	_, e = f.m.Spawn(ctx, runs.SpawnRequest{Role: "impl", Task: "t", Plan: []proto.PlanStep{{ID: "s1"}}})
	wantCode(t, e, proto.CodeInvalidPayload)

	_, e = f.m.Spawn(ctx, r)
	wantCode(t, e, proto.CodeAccountRequired)
	if !strings.Contains(e.Msg, "claude-b") || !strings.Contains(e.Msg, "codex-1") || strings.Contains(e.Msg, "off") {
		t.Fatalf("msg should list enabled accounts: %s", e.Msg)
	}
	r.Account = "nope"
	_, e = f.m.Spawn(ctx, r)
	wantCode(t, e, proto.CodeUnknownAccount)
	r.Account = "off"
	_, e = f.m.Spawn(ctx, r)
	wantCode(t, e, proto.CodeAccountDisabled)
	r.Account, r.Role = "claude-b", "docs"
	_, e = f.m.Spawn(ctx, r)
	wantCode(t, e, proto.CodeRoleNotAllowed)
	r.Role, r.Model = "impl", "haiku"
	_, e = f.m.Spawn(ctx, r)
	wantCode(t, e, proto.CodeModelNotAllowed)

	// Order: disabled beats role and model.
	r = runs.SpawnRequest{Account: "off", Role: "weird", Task: "t", Model: "x"}
	_, e = f.m.Spawn(ctx, r)
	wantCode(t, e, proto.CodeAccountDisabled)
	// Order: role beats model.
	r = runs.SpawnRequest{Account: "claude-b", Role: "weird", Task: "t", Model: "x"}
	_, e = f.m.Spawn(ctx, r)
	wantCode(t, e, proto.CodeRoleNotAllowed)
}

func TestRoutingFallbackAndDefaultModel(t *testing.T) {
	f := newFixture(t, baseCfg())
	res, e := f.m.Spawn(context.Background(), runs.SpawnRequest{Role: "review", Task: "t"})
	if e != nil {
		t.Fatal(e)
	}
	if res.AccountID != "claude-b" || res.Model != "opus" || res.Kind != "claude" {
		t.Fatalf("%+v", res)
	}
	if res.AgentID != "cc-b-0001" || res.Branch != "baton/cc-b-0001" {
		t.Fatalf("%+v", res)
	}
	if !strings.HasPrefix(res.Env["BATON_BRIEF"], res.Worktree) {
		t.Fatal(res.Env)
	}
}

func TestCaps(t *testing.T) {
	f := newFixture(t, baseCfg())
	ctx := context.Background()
	if _, e := f.m.Spawn(ctx, req("codex-1")); e != nil {
		t.Fatal(e)
	}
	_, e := f.m.Spawn(ctx, req("codex-1"))
	wantCode(t, e, proto.CodeAccountBusy)
	if f.m.Live("codex-1") != 1 || f.m.Live("") != 1 || f.m.Live("claude-b") != 0 {
		t.Fatal("live counts")
	}
	// propose mode: no global cap (3rd live agent ok, MaxAgentsAuto = 2)
	for i := 0; i < 2; i++ {
		if _, e := f.m.Spawn(ctx, req("claude-b")); e != nil {
			t.Fatal(e)
		}
	}
	if f.m.Live("") != 3 {
		t.Fatal(f.m.Live(""))
	}
	// auto + human: still no global cap, account cap applies
	f.cfg.Hub.Mode = config.ModeAuto
	f.cfg.Accounts[0].MaxConcurrent = 5
	if _, e := f.m.Spawn(ctx, req("claude-b")); e != nil {
		t.Fatal(e)
	}
	// auto + conductor: global cap
	r := req("claude-b")
	r.Actor = "conductor"
	_, e = f.m.Spawn(ctx, r)
	wantCode(t, e, proto.CodeMaxAgents)
}

func TestConcurrentSpawnsRespectCap(t *testing.T) {
	cfg := baseCfg()
	cfg.Accounts[0].MaxConcurrent = 3
	f := newFixture(t, cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, busy := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.m.Spawn(context.Background(), req("claude-b"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case e == nil:
				ok++
			case e.Code == proto.CodeAccountBusy:
				busy++
			default:
				t.Errorf("unexpected %v", e)
			}
		}()
	}
	wg.Wait()
	if ok != 3 || busy != 5 {
		t.Fatalf("ok=%d busy=%d", ok, busy)
	}
}

func TestIdentityAndCallsigns(t *testing.T) {
	cfg := baseCfg()
	cfg.Accounts[0].MaxConcurrent = 50
	cfg.Accounts = append(cfg.Accounts, config.Account{ID: "ollama", Kind: "mykind", Enabled: true, MaxConcurrent: 1})
	f := newFixture(t, cfg)
	ctx := context.Background()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		res, e := f.m.Spawn(ctx, req("claude-b"))
		if e != nil {
			t.Fatal(e)
		}
		if seen[strings.ToLower(res.Callsign)] || res.Callsign == "" {
			t.Fatalf("dup callsign %q", res.Callsign)
		}
		seen[strings.ToLower(res.Callsign)] = true
	}
	res, e := f.m.Spawn(ctx, req("codex-1"))
	if e != nil || res.AgentID != "cx-1-0006" {
		t.Fatalf("%v %+v", e, res)
	}
	res, e = f.m.Spawn(ctx, req("ollama"))
	if e != nil || res.AgentID != "gn-ollama-0007" {
		t.Fatalf("%v %+v", e, res)
	}
	// custom callsign: unique among live
	r := req("claude-b")
	r.Callsign = "Zed"
	if _, e := f.m.Spawn(ctx, r); e != nil {
		t.Fatal(e)
	}
	r.Callsign = "zed"
	_, e = f.m.Spawn(ctx, r)
	wantCode(t, e, proto.CodeCallsignTaken)
	// counter persists across managers
	b, _ := os.ReadFile(filepath.Join(f.data, "agent.seq"))
	if strings.TrimSpace(string(b)) != "8" {
		t.Fatalf("seq file %q", b)
	}
	m2 := f.manager()
	res, e = m2.Spawn(ctx, req("claude-b"))
	if e != nil || !strings.HasSuffix(res.AgentID, "-0009") {
		t.Fatalf("%v %+v", e, res)
	}
}

func TestWorktreeBranchBriefEnv(t *testing.T) {
	f := newFixture(t, baseCfg())
	git(t, f.repo, "branch", "dev")
	r := req("claude-b")
	r.BaseBranch = "dev"
	r.Effort = "high"
	r.Plan = []proto.PlanStep{{ID: "s1", Title: "Write code", Acceptance: "go test passes"}}
	r.Scope = runs.Scope{Paths: []string{"internal/x"}, Forbidden: []string{"docs"}, AskBefore: []string{"deps"}}
	res, e := f.m.Spawn(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	if res.BaseBranch != "dev" {
		t.Fatal(res.BaseBranch)
	}
	if got := git(t, res.Worktree, "rev-parse", "--abbrev-ref", "HEAD"); got != res.Branch {
		t.Fatalf("branch %s", got)
	}
	if _, err := os.Stat(filepath.Join(res.Worktree, "a.txt")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(res.Worktree, "BATON_BRIEF.md"), filepath.Join(res.RunDir, "BATON_BRIEF.md")} {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != res.Brief {
			t.Fatalf("brief at %s: %v", p, err)
		}
	}
	for _, want := range []string{"# You are " + res.Callsign + " (cc-b-0001)", "Role: impl · Runtime: claude/opus · Account: Claude B",
		"Branch: baton/cc-b-0001", "do the thing", "1. [s1] Write code", "Done when: go test passes",
		"Allowed paths: internal/x", "Do not touch: docs", "Ask before: deps", "Mirror your internal todo list"} {
		if !strings.Contains(res.Brief, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
	if strings.Contains(git(t, res.Worktree, "status", "--porcelain"), "BATON_BRIEF") {
		t.Fatal("brief not excluded from git")
	}
	for _, k := range []string{"BATON_URL", "BATON_TOKEN", "BATON_RUN_ID", "BATON_AGENT_ID", "BATON_RUN_DIR", "BATON_BRIEF", "BATON_MODEL",
		"BATON_ASK_TIMEOUT_S", "BATON_POLL_INTERVAL_S", "BATON_HEARTBEAT_INTERVAL_S"} {
		if res.Env[k] == "" {
			t.Errorf("env %s empty", k)
		}
	}
	run := f.st.Get(res.RunID)
	if run == nil || !run.CheckToken(res.Env["BATON_TOKEN"]) {
		t.Fatal("token does not match the stored run")
	}
	rec := run.Record()
	if rec.Branch != res.Branch || rec.Worktree != res.Worktree || rec.Actor != "human" || rec.Effort != "high" || len(rec.Plan) != 1 {
		t.Fatalf("%+v", rec)
	}
	if _, err := json.Marshal(res); err != nil {
		t.Fatal(err)
	}
}

func TestCodexAgentsMd(t *testing.T) {
	f := newFixture(t, baseCfg())
	res, e := f.m.Spawn(context.Background(), req("codex-1"))
	if e != nil {
		t.Fatal(e)
	}
	b, err := os.ReadFile(filepath.Join(res.Worktree, "AGENTS.md"))
	if err != nil || string(b) != res.Brief {
		t.Fatalf("AGENTS.md: %v", err)
	}
	// existing AGENTS.md is kept
	os.WriteFile(filepath.Join(f.repo, "AGENTS.md"), []byte("mine\n"), 0o644)
	git(t, f.repo, "add", "-A")
	git(t, f.repo, "commit", "-m", "agents")
	f.cfg.Accounts[1].MaxConcurrent = 2
	res, e = f.m.Spawn(context.Background(), req("codex-1"))
	if e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(filepath.Join(res.Worktree, "AGENTS.md"))
	if strings.TrimSpace(string(b)) != "mine" || !strings.Contains(res.Brief, "did not change it") {
		t.Fatalf("AGENTS.md overwritten: %q", b)
	}
}

func TestCustomKindNotes(t *testing.T) {
	cfg := baseCfg()
	cfg.Accounts = append(cfg.Accounts, config.Account{ID: "x-1", Kind: "mine", Enabled: true, MaxConcurrent: 1})
	cfg.Kinds = map[string]config.CustomKind{"mine": {Notes: "custom notes here"}}
	f := newFixture(t, cfg)
	res, e := f.m.Spawn(context.Background(), req("x-1"))
	if e != nil || !strings.Contains(res.Brief, "custom notes here") || !strings.HasPrefix(res.AgentID, "gn-1-") {
		t.Fatalf("%v %+v", e, res)
	}
}

func TestRollbackOnBranchExists(t *testing.T) {
	f := newFixture(t, baseCfg())
	git(t, f.repo, "branch", "baton/cc-b-0001")
	_, e := f.m.Spawn(context.Background(), req("claude-b"))
	wantCode(t, e, proto.CodeGit)
	if _, err := os.Stat(filepath.Join(f.data, "worktrees", "cc-b-0001")); err == nil {
		t.Fatal("worktree left behind")
	}
	if len(f.st.List()) != 0 {
		t.Fatal("run left in store")
	}
	if git(t, f.repo, "branch", "--list", "baton/cc-b-0001") == "" {
		t.Fatal("pre-existing branch was deleted")
	}
	// the id was consumed; next spawn works and gets a fresh id
	res, e := f.m.Spawn(context.Background(), req("claude-b"))
	if e != nil || res.AgentID != "cc-b-0002" {
		t.Fatalf("%v %+v", e, res)
	}
}

func TestRollbackOnBadBase(t *testing.T) {
	f := newFixture(t, baseCfg())
	r := req("claude-b")
	r.BaseBranch = "no-such-branch"
	_, e := f.m.Spawn(context.Background(), r)
	wantCode(t, e, proto.CodeGit)
	if git(t, f.repo, "branch", "--list", "baton/*") != "" || len(f.st.List()) != 0 {
		t.Fatal("leftovers")
	}
	entries, _ := os.ReadDir(filepath.Join(f.data, "worktrees"))
	if len(entries) != 0 {
		t.Fatal("worktree dir left")
	}
}

func TestDryRunTouchesNothing(t *testing.T) {
	f := newFixture(t, baseCfg())
	r := req("claude-b")
	r.DryRun = true
	res, e := f.m.Spawn(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	if !res.DryRun || res.Brief == "" || res.Env != nil || res.AgentID != "cc-b-0001" {
		t.Fatalf("%+v", res)
	}
	if len(f.st.List()) != 0 {
		t.Fatal("store touched")
	}
	if _, err := os.Stat(filepath.Join(f.data, "agent.seq")); err == nil {
		t.Fatal("counter touched")
	}
	if _, err := os.Stat(filepath.Join(f.data, "worktrees")); err == nil {
		t.Fatal("worktrees dir created")
	}
	if git(t, f.repo, "branch", "--list", "baton/*") != "" {
		t.Fatal("branch created")
	}
	// dry run still enforces selection rules
	r.Account = "off"
	_, e = f.m.Spawn(context.Background(), r)
	wantCode(t, e, proto.CodeAccountDisabled)
}

func endRun(t *testing.T, f *fixture, runID string) {
	t.Helper()
	r := f.st.Get(runID)
	_, _, e := r.IngestEnvelope(proto.Envelope{V: 1, RunID: runID, AgentID: r.AgentID(), Type: "done",
		Payload: map[string]any{"status": "success", "summary": "ok"}, MsgID: "d1"}, proto.SrcCLI)
	if e != nil {
		t.Fatal(e)
	}
}

func TestDiffStatAndRemoveWorktree(t *testing.T) {
	f := newFixture(t, baseCfg())
	ctx := context.Background()
	res, e := f.m.Spawn(ctx, req("claude-b"))
	if e != nil {
		t.Fatal(e)
	}
	s, err := f.m.DiffStat(ctx, res.RunID)
	if err != nil || !strings.HasPrefix(s, "no changes") || !strings.HasSuffix(s, "0 uncommitted") {
		t.Fatalf("%q %v", s, err)
	}
	os.WriteFile(filepath.Join(res.Worktree, "b.txt"), []byte("x\ny\n"), 0o644)
	git(t, res.Worktree, "add", "b.txt")
	git(t, res.Worktree, "commit", "-m", "b")
	os.WriteFile(filepath.Join(res.Worktree, "c.txt"), []byte("z\n"), 0o644)
	s, err = f.m.DiffStat(ctx, res.RunID)
	if err != nil || !strings.Contains(s, "1 file changed") || !strings.Contains(s, "2 insertions") || !strings.HasSuffix(s, "1 uncommitted") {
		t.Fatalf("%q %v", s, err)
	}
	if _, err := f.m.DiffStat(ctx, "r_nope"); err == nil {
		t.Fatal("want error")
	}

	if err := f.m.RemoveWorktree(ctx, res.RunID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Worktree); err == nil {
		t.Fatal("worktree remains")
	}
	if git(t, f.repo, "branch", "--list", res.Branch) != "" {
		t.Fatal("branch remains")
	}
}

func TestCleanupDue(t *testing.T) {
	cfg := baseCfg()
	cfg.Accounts[0].MaxConcurrent = 5
	f := newFixture(t, cfg)
	ctx := context.Background()
	a, _ := f.m.Spawn(ctx, req("claude-b"))
	b, _ := f.m.Spawn(ctx, req("claude-b")) // stays live
	if a == nil || b == nil {
		t.Fatal("spawn")
	}
	endRun(t, f, a.RunID)
	now := time.Now()

	if got := f.m.CleanupDue(now.AddDate(0, 0, 100)); len(got) != 0 {
		t.Fatalf("keep policy: %v", got)
	}
	f.cfg.Hub.Worktrees = config.Worktrees{Cleanup: config.CleanupRemoveAfter, Days: 14}
	if got := f.m.CleanupDue(now.AddDate(0, 0, 13)); len(got) != 0 {
		t.Fatalf("too early: %v", got)
	}
	got := f.m.CleanupDue(now.AddDate(0, 0, 15))
	if len(got) != 1 || got[0] != a.RunID {
		t.Fatalf("after days: %v", got)
	}

	// remove_on_merge needs run.json "merged": true
	f.cfg.Hub.Worktrees = config.Worktrees{Cleanup: config.CleanupRemoveOnMerge}
	if got := f.m.CleanupDue(now); len(got) != 0 {
		t.Fatalf("unmerged: %v", got)
	}
	p := filepath.Join(a.RunDir, "run.json")
	raw, _ := os.ReadFile(p)
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["merged"] = true
	raw, _ = json.Marshal(m)
	os.WriteFile(p, raw, 0o644)
	st2, err := store.Open(f.data, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	f.st = st2
	f.m = f.manager()
	got = f.m.CleanupDue(now)
	if len(got) != 1 || got[0] != a.RunID {
		t.Fatalf("merged: %v", got)
	}
	// already removed worktrees are not reported again
	if err := f.m.RemoveWorktree(ctx, a.RunID, false); err != nil {
		t.Fatal(err)
	}
	if got := f.m.CleanupDue(now); len(got) != 0 {
		t.Fatalf("after removal: %v", got)
	}
}
