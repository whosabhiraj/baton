package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/config"
)

func sbFixture(t *testing.T) Sandbox {
	t.Helper()
	home := filepath.Join(t.TempDir(), "bhome")
	proj := filepath.Join(home, "projects", "p-1")
	for _, d := range []string{"accounts/a", "projects/other", "projects/p-1/runs/r1", "projects/p-1/runs/r2", "projects/p-1/worktrees/w1", "projects/p-1/worktrees/w2", "projects/p-1/logs"} {
		if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(home, "config.yaml"), nil, 0o600)
	os.WriteFile(filepath.Join(proj, "admin.token"), []byte("x"), 0o600)
	return Sandbox{Worktree: filepath.Join(proj, "worktrees", "w1"), RunDir: filepath.Join(proj, "runs", "r1"),
		GitWrite: []string{filepath.Join(t.TempDir(), "repo", ".git", "objects")}, BatonHome: home,
		DenyRead: []string{filepath.Join(t.TempDir(), "secrets")}, GOOS: "linux"}
}

func TestDenyAround(t *testing.T) {
	sb := sbFixture(t)
	got := DenyAround(sb.BatonHome, []string{sb.Worktree, sb.RunDir})
	rel := func(p string) string { r, _ := filepath.Rel(sb.BatonHome, p); return filepath.ToSlash(r) }
	var names []string
	for _, p := range got {
		names = append(names, rel(p))
	}
	slices.Sort(names)
	want := []string{"accounts", "config.yaml", "projects/other", "projects/p-1/admin.token", "projects/p-1/logs", "projects/p-1/runs/r2", "projects/p-1/worktrees/w2"}
	if !slices.Equal(names, want) {
		t.Fatalf("got %v\nwant %v", names, want)
	}
	if out := DenyAround(sb.BatonHome, []string{filepath.Join(t.TempDir(), "elsewhere")}); len(out) != 1 || out[0] != filepath.Clean(sb.BatonHome) {
		t.Fatalf("nothing kept inside: %v", out)
	}
}

func TestClaudeSandboxArgv(t *testing.T) {
	sb := sbFixture(t)
	acct := config.Account{ID: "c1", Permissions: config.Permissions{Mode: config.ModeSandboxed, DeniedTools: []string{"WebFetch"}, ExtraArgs: []string{"--x"}}}
	sb.GOOS = "windows"
	if _, err := (claude{}).SandboxArgv(acct, "", "", sb); !errors.Is(err, ErrNoSandbox) || !strings.Contains(err.Error(), "permissive") {
		t.Fatalf("native windows: %v", err)
	}
	sb.GOOS = "linux"
	plan, err := (claude{}).SandboxArgv(acct, "sonnet", "low", sb)
	if err != nil {
		t.Fatal(err)
	}
	argv, files := plan.Argv, plan.Files
	if len(plan.Env) != 0 {
		t.Fatalf("claude gets no driver env: %v", plan.Env)
	}
	line := strings.Join(argv, " ")
	for _, want := range []string{"--model sonnet", "--effort low", "--permission-mode acceptEdits", "--permission-prompts none",
		"--settings " + filepath.Join(sb.RunDir, ClaudeSandboxFile), "--disallowedTools WebFetch"} {
		if !strings.Contains(line, want) {
			t.Errorf("argv lacks %q: %s", want, line)
		}
	}
	if argv[len(argv)-1] != "--x" || strings.Contains(line, "bypassPermissions") {
		t.Errorf("argv: %s", line)
	}
	var s struct {
		Sandbox struct {
			Enabled, FailIfUnavailable, AllowUnsandboxedCommands bool
			Filesystem                                           struct{ DenyRead, AllowRead, AllowWrite []string }
			Network                                              struct {
				AllowedDomains  []string
				StrictAllowlist bool
			}
		}
		Permissions struct{ Deny []string }
	}
	if err := json.Unmarshal(files[ClaudeSandboxFile], &s); err != nil {
		t.Fatal(err)
	}
	fs := s.Sandbox.Filesystem
	if !s.Sandbox.Enabled || !s.Sandbox.FailIfUnavailable || s.Sandbox.AllowUnsandboxedCommands || !s.Sandbox.Network.StrictAllowlist || len(s.Sandbox.Network.AllowedDomains) != 0 {
		t.Fatalf("sandbox: %+v", s.Sandbox)
	}
	if !slices.Contains(fs.DenyRead, sb.BatonHome) || !slices.Contains(fs.DenyRead, sb.DenyRead[0]) ||
		!slices.Contains(fs.AllowRead, sb.Worktree) || !slices.Contains(fs.AllowRead, sb.RunDir) ||
		!slices.Contains(fs.AllowWrite, sb.Worktree) || !slices.Contains(fs.AllowWrite, sb.RunDir) || !slices.Contains(fs.AllowWrite, sb.GitWrite[0]) {
		t.Fatalf("filesystem: %+v", fs)
	}
	deny := strings.Join(s.Permissions.Deny, "\n")
	token := filepath.ToSlash(filepath.Join(sb.BatonHome, "projects", "p-1", "admin.token"))
	if !strings.Contains(deny, "Read(/"+token+"/**)") || !strings.Contains(deny, "Edit(/"+filepath.ToSlash(sb.DenyRead[0])+"/**)") {
		t.Fatalf("deny rules miss the token or deny_read:\n%s", deny)
	}
	if strings.Contains(deny, filepath.ToSlash(sb.Worktree)) || strings.Contains(deny, filepath.ToSlash(sb.RunDir)+"/") {
		t.Fatalf("deny rules cover the agent's own worktree or run dir:\n%s", deny)
	}
	if _, err := (claude{}).Argv(acct, "", ""); err == nil {
		t.Fatal("Argv must refuse sandboxed mode")
	}
}

func TestCodexSandboxArgv(t *testing.T) {
	sb := sbFixture(t)
	acct := config.Account{ID: "x1", Permissions: config.Permissions{Mode: config.ModeSandboxed}}
	for _, goos := range []string{"windows", "linux"} {
		sb.GOOS = goos
		plan, err := (codex{}).SandboxArgv(acct, "", "medium", sb)
		if err != nil || plan.Files != nil || plan.Env != nil {
			t.Fatal(err, plan)
		}
		argv := plan.Argv
		line := strings.Join(argv, " ")
		if argv[len(argv)-1] != "-" || strings.Contains(line, "dangerously") || !strings.Contains(line, `default_permissions="baton"`) ||
			!strings.Contains(line, `approval_policy="never"`) || !strings.Contains(line, `model_reasoning_effort="medium"`) {
			t.Fatalf("%s: %s", goos, line)
		}
		if strings.Contains(line, "'"+sb.BatonHome+"'=") || strings.Contains(line, "'"+sb.Worktree+"'=") {
			t.Fatalf("%s: the worktree's parents must not be denied: %s", goos, line)
		}
		if mxc := strings.Contains(line, `windows.sandbox="mxc"`); mxc != (goos == "windows") {
			t.Fatalf("%s mxc=%v", goos, mxc)
		}
		for _, want := range []string{`":root"="read"`, `":workspace_roots"="write"`, "'" + sb.RunDir + `'="write"`,
			"'" + sb.GitWrite[0] + `'="write"`, "'" + filepath.Join(sb.BatonHome, "projects", "p-1", "admin.token") + `'="deny"`,
			"'" + filepath.Join(sb.BatonHome, "accounts") + `'="deny"`, "'" + sb.DenyRead[0] + `'="deny"`} {
			if !strings.Contains(line, want) {
				t.Fatalf("%s: lacks %s in %s", goos, want, line)
			}
		}
	}
	sb.DenyRead = []string{"/tmp/it's"}
	if _, err := (codex{}).SandboxArgv(acct, "", "", sb); err == nil {
		t.Fatal("a quote in a path must be refused")
	}
	if _, err := (codex{}).Argv(acct, "", ""); err == nil {
		t.Fatal("Argv must refuse sandboxed mode")
	}
}

// TestCodexDriverArgv: with permissions.ask the agent is `baton __codex` (app-server driver),
// which alone gets the hub URL and token; the sandbox overrides are the same.
func TestCodexDriverArgv(t *testing.T) {
	sb := sbFixture(t)
	sb.GOOS, sb.Driver, sb.HubURL, sb.HubToken = "windows", "/bin/baton", "http://127.0.0.1:7777", "bt_run_x"
	acct := config.Account{ID: "x1", Permissions: config.Permissions{Mode: config.ModeSandboxed, Ask: true, ExtraArgs: []string{"--e"}}}
	plan, err := (codex{}).SandboxArgv(acct, "gpt-x", "xhigh", sb)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(plan.Argv, " ")
	if plan.Argv[0] != "/bin/baton" || plan.Argv[1] != "__codex" || plan.Argv[len(plan.Argv)-1] != "--e" ||
		!strings.Contains(line, `model="gpt-x"`) || !strings.Contains(line, `model_reasoning_effort="high"`) ||
		!strings.Contains(line, `windows.sandbox="mxc"`) || !strings.Contains(line, `default_permissions="baton"`) ||
		strings.Contains(line, "approval_policy") || strings.Contains(line, " exec ") || strings.Contains(line, " - ") ||
		!strings.Contains(line, "features.network_proxy=true -c permissions.baton.network={enabled=true}") {
		t.Fatalf("driver argv: %s", line)
	}
	if plan.Env["BATON_DRIVER_URL"] != sb.HubURL || plan.Env["BATON_DRIVER_TOKEN"] != sb.HubToken {
		t.Fatalf("driver env: %v", plan.Env)
	}
	sb.Driver = "" // no driver known: plain exec
	if plan, _ := (codex{}).SandboxArgv(acct, "", "", sb); plan.Argv[0] != "codex" || strings.Contains(strings.Join(plan.Argv, " "), "network") {
		t.Fatalf("fallback: %v", plan.Argv)
	}
}

func TestCodexAppParser(t *testing.T) {
	p := codex{}.Parser()
	lines := []string{
		`{"method":"item/started","params":{"item":{"type":"commandExecution","command":"/bin/bash -lc 'go test ./...'"}}}`,
		`{"method":"item/completed","params":{"item":{"type":"fileChange","changes":[{"path":"a.go"},{"path":"b.go"}]}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"total":{"inputTokens":100,"cachedInputTokens":60,"cacheWriteInputTokens":5,"outputTokens":7},"last":{}}}}`,
		`{"method":"baton/access","params":{"kind":"command","summary":"needs network","status":"approved"}}`,
		`{"method":"item/agentMessage/delta","params":{"delta":"x"}}`,
		`{"method":"turn/completed","params":{"turn":{"id":"t","status":"completed"}}}`,
	}
	var got []string
	for _, l := range lines {
		for _, e := range p.Parse([]byte(l)) {
			got = append(got, e.Type+":"+fmt.Sprint(e.Payload["detail"], e.Payload["input_tokens"]))
		}
	}
	want := []string{"activity:go test ./...<nil>", "activity:a.go, b.go<nil>", "usage:<nil> 40",
		"activity:access request (command, approved): needs network<nil>", "finished:<nil> <nil>"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}
