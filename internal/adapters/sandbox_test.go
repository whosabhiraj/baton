package adapters

import (
	"encoding/json"
	"errors"
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
	if _, _, err := (claude{}).SandboxArgv(acct, "", "", sb); !errors.Is(err, ErrNoSandbox) || !strings.Contains(err.Error(), "permissive") {
		t.Fatalf("native windows: %v", err)
	}
	sb.GOOS = "linux"
	argv, files, err := (claude{}).SandboxArgv(acct, "sonnet", "low", sb)
	if err != nil {
		t.Fatal(err)
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
		argv, files, err := (codex{}).SandboxArgv(acct, "", "medium", sb)
		if err != nil || files != nil {
			t.Fatal(err, files)
		}
		line := strings.Join(argv, " ")
		if argv[len(argv)-1] != "-" || strings.Contains(line, "dangerously") || !strings.Contains(line, `default_permissions="baton"`) ||
			!strings.Contains(line, `approval_policy="never"`) || !strings.Contains(line, `model_reasoning_effort="medium"`) {
			t.Fatalf("%s: %s", goos, line)
		}
		if mxc := strings.Contains(line, `windows.sandbox="mxc"`); mxc != (goos == "windows") {
			t.Fatalf("%s mxc=%v", goos, mxc)
		}
		for _, want := range []string{`":root"="read"`, `":workspace_roots"="write"`, "'" + sb.RunDir + `'="write"`,
			"'" + sb.GitWrite[0] + `'="write"`, "'" + sb.BatonHome + `'="deny"`, "'" + sb.DenyRead[0] + `'="deny"`} {
			if !strings.Contains(line, want) {
				t.Fatalf("%s: lacks %s in %s", goos, want, line)
			}
		}
	}
	sb.DenyRead = []string{"/tmp/it's"}
	if _, _, err := (codex{}).SandboxArgv(acct, "", "", sb); err == nil {
		t.Fatal("a quote in a path must be refused")
	}
	if _, err := (codex{}).Argv(acct, "", ""); err == nil {
		t.Fatal("Argv must refuse sandboxed mode")
	}
}
