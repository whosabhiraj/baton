package config

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/whosabhiraj/baton/internal/proto"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadStr(t *testing.T, global, project string) *Snapshot {
	t.Helper()
	d := t.TempDir()
	gp, pp := filepath.Join(d, "g.yaml"), filepath.Join(d, "p.yaml")
	if global != "" {
		write(t, gp, global)
	}
	if project != "" {
		write(t, pp, project)
	}
	return Load(gp, pp)
}

func findIssue(s *Snapshot, path string) *Issue {
	for i := range s.Issues {
		if s.Issues[i].Path == path {
			return &s.Issues[i]
		}
	}
	return nil
}

func TestDefaultsAndMissingFiles(t *testing.T) {
	s := Load(filepath.Join(t.TempDir(), "nope.yaml"), "")
	if !s.OK() || len(s.Issues) != 0 || s.Version != 1 {
		t.Fatalf("%+v", s)
	}
	c := s.Config
	h := c.Hub
	if h.Port != 7777 || h.Bind != "127.0.0.1" || h.HeartbeatTimeoutS != 120 || h.AskTimeoutS != 900 ||
		h.PollIntervalS != 2 || h.IdleStopMin != 30 || !h.OpenBrowser || h.Mode != ModePropose ||
		h.MaxAgentsAuto != 6 || h.Worktrees.Cleanup != CleanupKeep || h.Worktrees.Days != 14 ||
		!h.Approvals.Merge || h.Approvals.Spawn {
		t.Fatalf("defaults: %+v", h)
	}
	if c.Accounts == nil || c.Routing == nil || c.Kinds == nil {
		t.Fatal("nil collections")
	}
	if !strings.HasSuffix(filepath.ToSlash(GlobalPath()), "/config.yaml") || ProjectPath("/r") != filepath.Join("/r", "baton.config.yaml") {
		t.Fatal("paths")
	}
}

func TestUnsetVsFalse(t *testing.T) {
	s := loadStr(t, `
hub:
  open_browser: false
  idle_stop_min: 0
  worktrees: {days: 3}
accounts:
  - {id: a, kind: claude}
  - {id: b, kind: claude, enabled: false, max_concurrent: 4}
`, "")
	if !s.OK() {
		t.Fatalf("%+v", s.Issues)
	}
	h := s.Config.Hub
	if h.OpenBrowser || h.IdleStopMin != 0 || h.Worktrees.Days != 3 || h.Worktrees.Cleanup != CleanupKeep || h.Port != 7777 {
		t.Fatalf("%+v", h)
	}
	a, _ := s.Config.Account("a")
	b, _ := s.Config.Account("b")
	if !a.Enabled || a.MaxConcurrent != 1 || a.Permissions.Mode != "default" {
		t.Fatalf("a %+v", a)
	}
	if b.Enabled || b.MaxConcurrent != 4 {
		t.Fatalf("b %+v", b)
	}
	if _, ok := s.Config.Account("zz"); ok {
		t.Fatal("unexpected account")
	}
}

func TestMerge(t *testing.T) {
	s := loadStr(t, `
hub:
  port: 8000
  mode: user
  worktrees: {cleanup: remove_on_merge, days: 5}
conductor: {account: a, model: opus}
accounts:
  - id: a
    kind: claude
    label: GA
    max_concurrent: 2
    env: {X: "1", Y: "2"}
    models: [opus]
  - {id: b, kind: codex}
routing: {reviewer: a, tester: b}
kinds:
  k1: {command: one}
`, `
hub:
  mode: auto
  worktrees: {days: 9}
conductor: {model: sonnet}
accounts:
  - id: a
    max_concurrent: 5
    enabled: false
    env: {Y: "3"}
  - {id: c, kind: generic}
routing: {tester: a}
kinds:
  k2: {command: two, parser: jsonl}
`)
	if !s.OK() {
		t.Fatalf("%+v", s.Issues)
	}
	c := s.Config
	if c.Hub.Port != 8000 || c.Hub.Mode != ModeAuto || c.Hub.Worktrees.Cleanup != CleanupRemoveOnMerge || c.Hub.Worktrees.Days != 9 {
		t.Fatalf("hub %+v", c.Hub)
	}
	if c.Conductor.Account != "a" || c.Conductor.Model != "sonnet" {
		t.Fatalf("conductor %+v", c.Conductor)
	}
	if len(c.Accounts) != 3 || c.Accounts[0].ID != "a" || c.Accounts[1].ID != "b" || c.Accounts[2].ID != "c" {
		t.Fatalf("accounts %+v", c.Accounts)
	}
	a := c.Accounts[0]
	if a.Label != "GA" || a.Kind != "claude" || a.MaxConcurrent != 5 || a.Enabled || a.Env["X"] != "1" || a.Env["Y"] != "3" || len(a.Models) != 1 {
		t.Fatalf("a %+v", a)
	}
	if c.Routing["reviewer"] != "a" || c.Routing["tester"] != "a" || len(c.Kinds) != 2 || c.Kinds["k1"].Parser != "none" || c.Kinds["k2"].Parser != "jsonl" {
		t.Fatalf("routing/kinds %+v %+v", c.Routing, c.Kinds)
	}
}

func TestValidationRules(t *testing.T) {
	acct := func(extra string) string { return "accounts:\n  - {id: a, kind: claude" + extra + "}\n" }
	tests := []struct {
		name, yaml, path string
		line             int
	}{
		{"id pattern", "accounts:\n  - {id: Bad_ID, kind: claude}\n", "accounts[0].id", 2},
		{"id dup", "accounts:\n  - {id: a, kind: claude}\n  - {id: a, kind: codex}\n", "accounts[1].id", 3},
		{"kind", "accounts:\n  - {id: a, kind: bogus}\n", "accounts[0].kind", 2},
		{"color", acct(", color: red"), "accounts[0].color", 2},
		{"max_concurrent", acct(", max_concurrent: 0"), "accounts[0].max_concurrent", 2},
		{"perm mode", acct(", permissions: {mode: yolo}"), "accounts[0].permissions.mode", 2},
		{"account timeout", acct(", ask_timeout_s: -1"), "accounts[0].ask_timeout_s", 2},
		{"port high", "hub:\n  port: 70000\n", "hub.port", 2},
		{"port neg", "hub:\n  port: -1\n", "hub.port", 2},
		{"bind", "hub:\n  bind: 0.0.0.0\n", "hub.bind", 2},
		{"mode", "hub:\n  mode: robot\n", "hub.mode", 2},
		{"cleanup", "hub:\n  worktrees:\n    cleanup: nuke\n", "hub.worktrees.cleanup", 3},
		{"days", "hub:\n  worktrees:\n    days: 0\n", "hub.worktrees.days", 3},
		{"heartbeat", "hub:\n  heartbeat_timeout_s: 0\n", "hub.heartbeat_timeout_s", 2},
		{"routing target", "routing:\n  reviewer: ghost\n", "routing.reviewer", 2},
		{"conductor account", "conductor:\n  account: ghost\n", "conductor.account", 2},
		{"parser", "kinds:\n  k:\n    command: x\n    parser: xml\n", "kinds.k.parser", 4},
		{"kind command", "kinds:\n  k:\n    parser: jsonl\n", "kinds.k.command", 3},
		{"type error", "hub:\n  port: abc\n", "hub", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := loadStr(t, tc.yaml, "")
			if s.OK() {
				t.Fatalf("expected error, got none: %+v", s.Config.Hub)
			}
			is := findIssue(s, tc.path)
			if is == nil {
				t.Fatalf("no issue at %s: %+v", tc.path, s.Issues)
			}
			if is.Line != tc.line || is.File == "" || is.Msg == "" {
				t.Fatalf("issue %+v, want line %d", is, tc.line)
			}
			if s.Config.Hub.Port != 7777 || len(s.Config.Accounts) != 0 {
				t.Fatal("invalid config should fall back to defaults")
			}
		})
	}
}

func TestCustomKindAccountValid(t *testing.T) {
	s := loadStr(t, "kinds:\n  mine: {command: x}\naccounts:\n  - {id: a, kind: mine}\n", "")
	if !s.OK() {
		t.Fatal(s.Issues)
	}
}

func TestIssueFileIsOriginFile(t *testing.T) {
	d := t.TempDir()
	gp, pp := filepath.Join(d, "g.yaml"), filepath.Join(d, "p.yaml")
	write(t, gp, "accounts:\n  - {id: a, kind: claude}\n")
	write(t, pp, "\n\naccounts:\n  - id: a\n    max_concurrent: 0\n")
	s := Load(gp, pp)
	is := findIssue(s, "accounts[0].max_concurrent")
	if is == nil || is.File != pp || is.Line != 5 {
		t.Fatalf("%+v", s.Issues)
	}
}

func TestUnknownKeysAreWarnings(t *testing.T) {
	s := loadStr(t, "wat: 1\nhub:\n  prot: 1\naccounts:\n  - {id: a, kind: claude, colour: red}\n", "")
	if !s.OK() || s.Version != 1 {
		t.Fatalf("should load: %+v", s.Issues)
	}
	for _, p := range []string{"wat", "hub.prot", "accounts[0].colour"} {
		is := findIssue(s, p)
		if is == nil || !strings.HasPrefix(is.Msg, "unknown key") || !is.Warning() || is.Line == 0 {
			t.Fatalf("%s: %+v", p, s.Issues)
		}
	}
}

func TestOpencodeForced(t *testing.T) {
	s := loadStr(t, "accounts:\n  - {id: o, kind: opencode}\n  - {id: p, kind: opencode, forbid_claude_subscription: false}\n  - {id: q, kind: claude}\n", "")
	if !s.OK() {
		t.Fatal(s.Issues)
	}
	if !s.Config.Accounts[0].ForbidClaudeSubscription || !s.Config.Accounts[1].ForbidClaudeSubscription || s.Config.Accounts[2].ForbidClaudeSubscription {
		t.Fatalf("%+v", s.Config.Accounts)
	}
	if findIssue(s, "accounts[0].forbid_claude_subscription") != nil {
		t.Fatal("unset should be silent")
	}
	is := findIssue(s, "accounts[1].forbid_claude_subscription")
	if is == nil || !is.Warning() {
		t.Fatalf("%+v", s.Issues)
	}
}

func TestEnvTildeExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	s := loadStr(t, "accounts:\n  - id: a\n    kind: claude\n    env: {A: \"~/x/y\", B: \"a~b\", C: \"$HOME/z\", D: \"~\", E: \"~user/q\"}\n", "")
	e := s.Config.Accounts[0].Env
	if e["A"] != filepath.Join(home, "x", "y") || e["B"] != "a~b" || e["C"] != "$HOME/z" || e["D"] != home || e["E"] != "~user/q" {
		t.Fatalf("%v", e)
	}
}

func TestParseErrorFallsBack(t *testing.T) {
	s := loadStr(t, "hub: [unclosed\n", "")
	if s.OK() || s.Issues[0].Line == 0 || s.Issues[0].File == "" || s.Config.Hub.Port != 7777 {
		t.Fatalf("%+v", s)
	}
	s = loadStr(t, "- a\n- b\n", "")
	if s.OK() {
		t.Fatal("non-mapping accepted")
	}
}

func TestWatchReloadAndLastGood(t *testing.T) {
	d := t.TempDir()
	gp := filepath.Join(d, "g.yaml")
	write(t, gp, "hub:\n  port: 8001\n")
	var calls atomic.Int32
	w := Watch(context.Background(), gp, "", 20*time.Millisecond, func(*Snapshot) { calls.Add(1) })
	defer w.Stop()
	if w.Current().Config.Hub.Port != 8001 || w.Current().Version != 1 {
		t.Fatalf("%+v", w.Current())
	}
	wait := func(cond func(*Snapshot) bool) *Snapshot {
		t.Helper()
		for i := 0; i < 200; i++ {
			if s := w.Current(); cond(s) {
				return s
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timeout: %+v", w.Current())
		return nil
	}
	time.Sleep(80 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("onChange without a change")
	}
	write(t, gp, "hub:\n  port: 8002\n")
	s := wait(func(s *Snapshot) bool { return s.Config.Hub.Port == 8002 })
	if s.Version != 2 {
		t.Fatalf("version %d", s.Version)
	}
	// broken file: last good kept, issues reported, version unchanged
	write(t, gp, "hub:\n  port: 99999\n")
	s = wait(func(s *Snapshot) bool { return !s.OK() })
	if s.Config.Hub.Port != 8002 || s.Version != 2 || findIssue(s, "hub.port") == nil {
		t.Fatalf("%+v", s)
	}
	// fixed
	write(t, gp, "hub:\n  port: 8003\n")
	s = wait(func(s *Snapshot) bool { return s.OK() && s.Config.Hub.Port == 8003 })
	if s.Version != 3 || calls.Load() < 3 {
		t.Fatalf("version %d calls %d", s.Version, calls.Load())
	}
	// file removal -> defaults
	os.Remove(gp)
	wait(func(s *Snapshot) bool { return s.Config.Hub.Port == 7777 })
	w.Stop()
	w.Stop()
	(&Watcher{}).Stop()
}

func TestWatchInitialInvalidUsesDefaults(t *testing.T) {
	gp := filepath.Join(t.TempDir(), "g.yaml")
	write(t, gp, "hub:\n  mode: robot\n")
	w := Watch(context.Background(), gp, "", 0, nil)
	defer w.Stop()
	if w.Current().OK() || w.Current().Config.Hub.Mode != ModePropose || w.Current().Version != 0 {
		t.Fatalf("%+v", w.Current())
	}
}

const commented = `# Global Baton config
hub:
  mode: propose # how lineups are decided
  open_browser: true

# the accounts
accounts:
  # first account
  - id: a
    kind: claude
    max_concurrent: 2 # keep low
    models: [opus, sonnet]
    roles:
      - planner
  - id: b
    kind: codex
`

func TestSetAccountFieldPreservesComments(t *testing.T) {
	d := t.TempDir()
	gp := filepath.Join(d, "sub", "g.yaml")
	if err := os.MkdirAll(filepath.Dir(gp), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, gp, commented)
	snap := Load(gp, "")
	if !snap.OK() {
		t.Fatal(snap.Issues)
	}
	if err := SetAccountField(snap, "a", "max_concurrent", 3); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountField(snap, "a", "models", []string{"haiku"}); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountField(snap, "b", "enabled", false); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountField(snap, "b", "label", "B: the second"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(gp)
	// yaml.v3 round-trips comments and order but drops blank lines.
	want := `# Global Baton config
hub:
  mode: propose # how lineups are decided
  open_browser: true
# the accounts
accounts:
  # first account
  - id: a
    kind: claude
    max_concurrent: 3 # keep low
    models: [haiku]
    roles:
      - planner
  - id: b
    kind: codex
    enabled: false
    label: 'B: the second'
`
	if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	re := Load(gp, "")
	if !re.OK() {
		t.Fatal(re.Issues)
	}
	b, _ := re.Config.Account("b")
	a, _ := re.Config.Account("a")
	if b.Enabled || a.MaxConcurrent != 3 || a.Models[0] != "haiku" {
		t.Fatalf("%+v %+v", a, b)
	}
	// no temp files left
	ents, _ := os.ReadDir(filepath.Dir(gp))
	if len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestSetAccountFieldTargetsProjectFile(t *testing.T) {
	d := t.TempDir()
	gp, pp := filepath.Join(d, "g.yaml"), filepath.Join(d, "p.yaml")
	write(t, gp, "accounts:\n  - {id: a, kind: claude}\n  - {id: b, kind: claude}\n")
	write(t, pp, "accounts:\n  - id: a\n    max_concurrent: 2\n  - {id: c, kind: generic}\n")
	snap := Load(gp, pp)
	if err := SetAccountField(snap, "a", "max_concurrent", 4); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountField(snap, "b", "max_concurrent", 5); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountField(snap, "c", "enabled", false); err != nil {
		t.Fatal(err)
	}
	s := Load(gp, pp)
	for id, n := range map[string]int{"a": 4, "b": 5, "c": 1} {
		a, _ := s.Config.Account(id)
		if a.MaxConcurrent != n {
			t.Fatalf("%s: %d", id, a.MaxConcurrent)
		}
	}
	gb, _ := os.ReadFile(gp)
	if strings.Contains(string(gb), "max_concurrent: 4") {
		t.Fatalf("a was written to the global file:\n%s", gb)
	}
	if err := SetAccountField(snap, "ghost", "enabled", true); err == nil {
		t.Fatal("unknown account accepted")
	}
	if err := SetAccountField(snap, "a", "bogus", 1); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := SetAccountField(snap, "a", "id", "z"); err == nil {
		t.Fatal("id change accepted")
	}
}

func TestSetFieldRefusedWhenInvalid(t *testing.T) {
	d := t.TempDir()
	gp := filepath.Join(d, "g.yaml")
	write(t, gp, commented)
	snap := Load(gp, "")
	before, _ := os.ReadFile(gp)
	err := SetAccountField(snap, "a", "max_concurrent", 0)
	if err == nil || !strings.Contains(err.Error(), "accounts[0].max_concurrent") {
		t.Fatalf("err %v", err)
	}
	if err := SetAccountField(snap, "a", "color", "nope"); err == nil {
		t.Fatal("bad color accepted")
	}
	if err := SetHubField(snap, "mode", "robot"); err == nil || !strings.Contains(err.Error(), "hub.mode") {
		t.Fatalf("err %v", err)
	}
	if err := SetAccountField(snap, "a", "max_concurrent", "many"); err == nil {
		t.Fatal("wrong type accepted")
	}
	after, _ := os.ReadFile(gp)
	if !bytes.Equal(before, after) {
		t.Fatal("file changed by refused edit")
	}
}

func TestSetHubField(t *testing.T) {
	d := t.TempDir()
	home := filepath.Join(d, "home", ".baton")
	gp := filepath.Join(home, "config.yaml") // does not exist, dir does not exist
	pp := filepath.Join(d, "p.yaml")
	snap := Load(gp, pp)
	if err := SetHubField(snap, "open_browser", false); err != nil {
		t.Fatal(err)
	}
	if err := SetHubField(snap, "worktrees.cleanup", "remove_on_merge"); err != nil {
		t.Fatal(err)
	}
	if err := SetHubField(snap, "bogus", 1); err == nil {
		t.Fatal("unknown hub field accepted")
	}
	if err := SetHubField(snap, "worktrees", "x"); err == nil {
		t.Fatal("section accepted")
	}
	s := Load(gp, pp)
	if !s.OK() || s.Config.Hub.OpenBrowser || s.Config.Hub.Worktrees.Cleanup != CleanupRemoveOnMerge {
		t.Fatalf("%+v %v", s.Config.Hub, s.Issues)
	}
	if _, err := os.Stat(pp); err == nil {
		t.Fatal("project file created though it did not exist")
	}
	// project file that sets the key gets the write
	write(t, pp, "hub:\n  mode: user # mine\n")
	snap = Load(gp, pp)
	if err := SetHubField(snap, "mode", "auto"); err != nil {
		t.Fatal(err)
	}
	pb, _ := os.ReadFile(pp)
	if !strings.Contains(string(pb), "mode: auto # mine") {
		t.Fatalf("project:\n%s", pb)
	}
	// project file exists but does not set the key -> global
	if err := SetHubField(snap, "ask_timeout_s", 60); err != nil {
		t.Fatal(err)
	}
	s = Load(gp, pp)
	if s.Config.Hub.AskTimeoutS != 60 || s.Config.Hub.Mode != ModeAuto {
		t.Fatalf("%+v", s.Config.Hub)
	}
	gb, _ := os.ReadFile(gp)
	if !strings.Contains(string(gb), "ask_timeout_s: 60") {
		t.Fatalf("global:\n%s", gb)
	}
}

func TestExampleYAML(t *testing.T) {
	path := filepath.Join("..", "..", "baton.config.example.yaml")
	s := Load(path, "")
	for _, i := range s.Issues {
		t.Errorf("issue: %+v", i)
	}
	c := s.Config
	if len(c.Accounts) != 4 || c.Accounts[3].ID != "opencode-k" || c.Accounts[3].Enabled || !c.Accounts[3].ForbidClaudeSubscription {
		t.Fatalf("%+v", c.Accounts)
	}
	if c.Routing["reviewer"] != "claude-a" || c.Conductor.Account != "claude-a" {
		t.Fatalf("%+v", c)
	}
	// and the file validates against the published JSON schema
	b, _ := os.ReadFile(path)
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(v)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.ValidateJSON("config", doc); err != nil {
		t.Fatalf("schema: %v", err)
	}
}

func TestWatchReloadNow(t *testing.T) {
	gp := filepath.Join(t.TempDir(), "g.yaml")
	write(t, gp, "hub:\n  port: 8001\n")
	var calls atomic.Int32
	w := Watch(context.Background(), gp, "", time.Hour, func(*Snapshot) { calls.Add(1) })
	defer w.Stop()
	if s := w.Reload(); s.Version != 1 || calls.Load() != 0 {
		t.Fatalf("no change: %+v calls=%d", s, calls.Load())
	}
	write(t, gp, "hub:\n  port: 8002\n")
	if s := w.Reload(); s.Config.Hub.Port != 8002 || s.Version != 2 || calls.Load() != 1 {
		t.Fatalf("changed: %+v calls=%d", s, calls.Load())
	}
	if s := w.Reload(); s.Version != 2 || calls.Load() != 1 {
		t.Fatal("second reload must be a no-op")
	}
}
