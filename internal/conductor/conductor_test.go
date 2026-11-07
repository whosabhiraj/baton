package conductor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/bus"
	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/proto"
)

// TestMain doubles as the fake `claude` (FakeCLI) when the Manager runs the test binary.
func TestMain(m *testing.M) {
	if os.Getenv(FakeEnv) == "1" {
		os.Exit(FakeCLI(os.Args[1:], os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

type fixture struct {
	t      *testing.T
	data   string
	args   string
	mu     sync.Mutex
	cfg    config.Config
	states []string
	tokens int
	revoke int
	bus    *bus.Broker
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, data: t.TempDir()}
	f.args = filepath.Join(f.data, "args.jsonl")
	f.cfg = config.Defaults()
	f.cfg.Accounts = []config.Account{{ID: "c1", Kind: "claude", Enabled: true, MaxConcurrent: 1,
		Env: map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(f.data, "acct")}}}
	return f
}

func (f *fixture) manager(o ...func(*Options)) *Manager {
	f.t.Helper()
	opts := Options{DataDir: f.data, Repo: f.data, Exe: filepath.Join(f.data, "baton.exe"), CLI: os.Args[0],
		HubURL: func() string { return "http://127.0.0.1:1" },
		Config: func() config.Config { f.mu.Lock(); defer f.mu.Unlock(); return f.cfg },
		Mint:   func() string { f.mu.Lock(); defer f.mu.Unlock(); f.tokens++; return "bt_cond_test" },
		Revoke: func() { f.mu.Lock(); defer f.mu.Unlock(); f.revoke++ },
		SetState: func(s proto.ConductorState) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.states = append(f.states, s.State)
		},
		ExtraEnv:  map[string]string{FakeEnv: "1", "BATON_FAKE_ARGS": f.args},
		WakeDelay: 20 * time.Millisecond, Backoff: []time.Duration{10 * time.Millisecond, 10 * time.Millisecond},
	}
	if f.bus != nil {
		opts.Bus = f.bus
	}
	for _, fn := range o {
		fn(&opts)
	}
	m, err := New(opts)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(m.Close)
	return m
}

func (f *fixture) argLines() [][]string {
	b, _ := os.ReadFile(f.args)
	var out [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var a []string
		json.Unmarshal([]byte(l), &a)
		out = append(out, a)
	}
	return out
}

func flag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func hasOut(m *Manager, sub string) bool {
	es, _, _ := m.History(0, 0)
	for _, e := range es {
		if e.Dir == "out" && strings.Contains(string(e.Msg), sub) {
			return true
		}
	}
	return false
}

func countIn(m *Manager, from string) int {
	es, _, _ := m.History(0, 0)
	n := 0
	for _, e := range es {
		if e.Dir == "in" && e.From == from {
			n++
		}
	}
	return n
}

func TestStartSendHistoryResume(t *testing.T) {
	f := newFixture(t)
	m := f.manager()
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Send("hello", "human"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "echo", func() bool { return hasOut(m, "echo: hello") && m.State().State == StateIdle })
	in := m.Info()
	if !in.Running || !in.Wanted || in.Account != "c1" || in.Model != "opus" || in.Effort != "high" || in.SessionID == "" {
		t.Fatalf("%+v", in)
	}
	f.mu.Lock()
	states := strings.Join(f.states, ",")
	f.mu.Unlock()
	if !strings.Contains(states, "idle,thinking") || !strings.Contains(states, "acting") || !strings.HasSuffix(states, "idle") {
		t.Fatal(states)
	}

	a := f.argLines()
	if len(a) != 1 {
		t.Fatal(a)
	}
	args := a[0]
	for _, want := range [][2]string{{"--input-format", "stream-json"}, {"--output-format", "stream-json"}, {"--model", "opus"},
		{"--effort", "high"}, {"--permission-prompts", "none"}, {"--allowedTools", "mcp__baton,Read,Grep,Glob"}, {"--tools", "Read,Grep,Glob,ToolSearch"},
		{"--session-id", in.SessionID}} {
		if flag(args, want[0]) != want[1] {
			t.Fatalf("%s: got %q in %v", want[0], flag(args, want[0]), args)
		}
	}
	if p := flag(args, "--append-system-prompt-file"); p == "" {
		t.Fatal("no prompt file")
	} else if b, _ := os.ReadFile(p); string(b) != Prompt {
		t.Fatal("prompt file differs")
	}
	var mcp struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	b, _ := os.ReadFile(flag(args, "--mcp-config"))
	if json.Unmarshal(b, &mcp) != nil || mcp.MCPServers["baton"].Env["BATON_CONDUCTOR_TOKEN"] != "bt_cond_test" ||
		strings.Join(mcp.MCPServers["baton"].Args, " ") != "mcp --url http://127.0.0.1:1" {
		t.Fatal(string(b))
	}

	// hub restart: the session is resumed
	sid := in.SessionID
	m.Close()
	m2 := f.manager()
	m2.Resume()
	waitFor(t, "resume", func() bool { return m2.Info().Running })
	if err := m2.Send("again", "human"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "echo again", func() bool { return hasOut(m2, "echo: again") })
	a = f.argLines()
	if len(a) != 2 || flag(a[1], "--resume") != sid || flag(a[1], "--session-id") != "" {
		t.Fatal(a)
	}

	es, last, _ := m2.History(0, 0)
	for i := 1; i < len(es); i++ {
		if es[i].N != es[i-1].N+1 {
			t.Fatal("history numbers not consecutive")
		}
	}
	if last != es[len(es)-1].N {
		t.Fatal(last)
	}
	if tail, _, _ := m2.History(last-2, 0); len(tail) != 2 {
		t.Fatal(tail)
	}
	if lim, _, _ := m2.History(0, 3); len(lim) != 3 || lim[2].N != last {
		t.Fatal(lim)
	}
}

func TestStopIsRemembered(t *testing.T) {
	f := newFixture(t)
	m := f.manager()
	m.Start()
	m.Stop()
	if m.Info().Running || m.Info().Wanted {
		t.Fatal("still running")
	}
	m.Close()
	m2 := f.manager()
	m2.Resume()
	time.Sleep(100 * time.Millisecond)
	if m2.Info().Running {
		t.Fatal("stopped conductor came back after restart")
	}
}

func TestCrashRestartsThenGivesUp(t *testing.T) {
	f := newFixture(t)
	m := f.manager()
	exits := func() int {
		es, _, _ := m.History(0, 0)
		n := 0
		for _, e := range es {
			if e.Event == "exited" {
				n++
			}
		}
		return n
	}
	for i := 1; i <= 3; i++ {
		// the first start is ours, the next ones are the automatic restarts
		waitFor(t, "running", func() bool { return (i > 1 || m.Start() == nil) && m.Info().Running })
		m.Send("crash", "human")
		waitFor(t, "exit", func() bool { return exits() == i })
	}
	waitFor(t, "give up", func() bool {
		es, _, _ := m.History(0, 0)
		return len(es) > 0 && es[len(es)-1].Event == "error"
	})
	time.Sleep(50 * time.Millisecond)
	if m.Info().Running || m.Info().LastError == "" {
		t.Fatalf("%+v", m.Info())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revoke < 3 {
		t.Fatal("token not revoked on exit", f.revoke)
	}
}

func TestConfigChangeRestartsIdleConductor(t *testing.T) {
	f := newFixture(t)
	m := f.manager()
	m.Send("one", "human")
	waitFor(t, "one", func() bool { return hasOut(m, "echo: one") && m.State().State == StateIdle })
	f.mu.Lock()
	f.cfg.Conductor.Model, f.cfg.Conductor.Effort = "sonnet", ""
	f.mu.Unlock()
	m.Send("two", "human")
	waitFor(t, "two", func() bool { return hasOut(m, "echo: two") })
	a := f.argLines()
	if len(a) != 2 || flag(a[1], "--model") != "sonnet" || flag(a[1], "--effort") != "" || flag(a[1], "--resume") == "" {
		t.Fatal(a)
	}
}

func TestAutoWake(t *testing.T) {
	f := newFixture(t)
	f.bus = bus.New(0, 0)
	m := f.manager()
	m.Start()
	waitFor(t, "running", func() bool { return m.Info().Running })
	time.Sleep(50 * time.Millisecond) // the wake loop subscribes asynchronously

	f.bus.Publish(proto.StoredEvent{Type: "progress"})
	time.Sleep(100 * time.Millisecond)
	if countIn(m, "hub") != 0 {
		t.Fatal("woke on progress")
	}
	f.bus.Publish(proto.StoredEvent{Type: "question"})
	f.bus.Publish(proto.StoredEvent{Type: "done"}) // coalesced into one nudge
	waitFor(t, "nudge", func() bool { return countIn(m, "hub") == 1 && hasOut(m, "echo: [baton]") })
	waitFor(t, "idle", func() bool { return m.State().State == StateIdle })

	// pending approval requests do not wake; decided ones do
	f.bus.PublishNotice("approval", map[string]any{"approval": approvals.Approval{Status: approvals.Pending}})
	time.Sleep(100 * time.Millisecond)
	if countIn(m, "hub") != 1 {
		t.Fatal("woke on a pending approval")
	}
	f.bus.PublishNotice("approval", map[string]any{"approval": approvals.Approval{Status: approvals.Approved}})
	waitFor(t, "approval nudge", func() bool { return countIn(m, "hub") == 2 })
	waitFor(t, "idle", func() bool { return m.State().State == StateIdle })

	// already read through wait_for_events (Seen): no nudge; something newer still wakes
	f.bus.Publish(proto.StoredEvent{Type: "exit"})
	m.Seen(f.bus.LastID())
	time.Sleep(150 * time.Millisecond)
	if countIn(m, "hub") != 2 {
		t.Fatal("woke for events the conductor already read")
	}
	f.bus.Publish(proto.StoredEvent{Type: "done"})
	waitFor(t, "nudge after newer event", func() bool { return countIn(m, "hub") == 3 })
	waitFor(t, "idle", func() bool { return m.State().State == StateIdle })

	f.mu.Lock()
	f.cfg.Conductor.AutoWake = false
	f.mu.Unlock()
	waitFor(t, "idle", func() bool { return m.State().State == StateIdle })
	f.bus.Publish(proto.StoredEvent{Type: "error"})
	time.Sleep(150 * time.Millisecond)
	if countIn(m, "hub") != 3 {
		t.Fatal("woke with auto_wake off")
	}
}

func TestResolveProblems(t *testing.T) {
	f := newFixture(t)
	f.cfg.Accounts = nil
	m := f.manager()
	if err := m.Start(); !errors.Is(err, ErrNoAccount) || m.Info().Problem == "" {
		t.Fatal(err, m.Info())
	}
	f.cfg.Accounts = []config.Account{{ID: "x1", Kind: "codex", Enabled: true}}
	f.cfg.Conductor.Account = "x1"
	if err := m.Start(); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	f.cfg.Accounts[0].Kind, f.cfg.Accounts[0].Enabled = "claude", false
	if err := m.Start(); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatal(err)
	}
	f.cfg.Accounts[0].Enabled = true
	m.o.CLI = "baton-test-no-such-cli"
	if err := m.Start(); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
}

// TestUsage: the latest result per session is that session's total (claude's running totals),
// sessions add up per account, and the totals survive a restart of the manager.
func TestUsage(t *testing.T) {
	f := newFixture(t)
	m := f.manager()
	m.Start()
	waitFor(t, "running", func() bool { return m.Info().Running })
	for _, s := range []string{"one", "two"} {
		if err := m.Send(s, "human"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "reply "+s, func() bool { return hasOut(m, "echo: "+s) && m.State().State == StateIdle })
	}
	waitFor(t, "usage", func() bool { u := m.Usage()["c1"]; return u != nil && u["input_tokens"] == 20.0 })
	if u := m.Usage()["c1"]; u["output_tokens"] != 10.0 || u["cost_usd"] != 0.0 {
		t.Fatalf("usage: %v", u)
	}

	m.mu.Lock() // a second session with real cost, then a later (cumulative) result of it
	m.noteResultLocked([]byte(`{"type":"result","session_id":"s2","total_cost_usd":0.5,"modelUsage":{"a":{"inputTokens":3,"outputTokens":4,"cacheReadInputTokens":5,"cacheCreationInputTokens":6}}}`))
	m.noteResultLocked([]byte(`{"type":"result","session_id":"s2","total_cost_usd":0.75,"modelUsage":{"a":{"inputTokens":4,"outputTokens":4},"b":{"inputTokens":1}}}`))
	m.noteResultLocked([]byte(`{"type":"result","session_id":"s3"}`)) // nothing to record
	m.mu.Unlock()
	u := m.Usage()["c1"]
	if u["input_tokens"] != 25.0 || u["cost_usd"] != 0.75 || len(m.Usage()) != 1 {
		t.Fatalf("summed: %v", m.Usage())
	}
	m.Close()

	m2 := f.manager()
	if got := m2.Usage()["c1"]; got["input_tokens"] != 25.0 || got["cost_usd"] != 0.75 {
		t.Fatalf("after restart: %v", got)
	}
}
