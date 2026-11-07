package launch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/launch"
	"github.com/whosabhiraj/baton/internal/runs"
)

// OpenCode must never run on a Claude subscription login: Start refuses before anything is
// launched, and the message does not leak the credential.
func TestErrPathsStartRefusesOpencodeWithClaudeLogin(t *testing.T) {
	f := newFx(t)
	const secret = "PLACEHOLDER-NOT-A-SECRET"
	xdg := t.TempDir()
	d := filepath.Join(xdg, "opencode")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	auth := `{"anthropic":{"type":"oauth","refresh":"` + secret + `","access":"y","expires":1}}`
	if err := os.WriteFile(filepath.Join(d, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	f.cfg.Accounts = append(f.cfg.Accounts, config.Account{ID: "oc", Kind: "opencode", Enabled: true, MaxConcurrent: 1,
		Roles: []string{"impl"}, Env: map[string]string{"XDG_DATA_HOME": xdg}})
	res := f.spawn("oc")
	err := f.launch.Start(*res)
	if err == nil || !strings.Contains(err.Error(), "Claude subscription") {
		t.Fatalf("want a Claude subscription refusal, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("refusal leaks a credential: %v", err)
	}
	// no process was started: no supervisor state for the run
	if _, serr := os.Stat(filepath.Join(res.RunDir, "supervisor.json")); serr == nil {
		t.Fatal("agent was launched despite the refusal")
	}

	// without an isolated XDG_DATA_HOME it cannot tell whose login it would use: refuse as well
	f.cfg.Accounts = append(f.cfg.Accounts, config.Account{ID: "oc2", Kind: "opencode", Enabled: true, MaxConcurrent: 1, Roles: []string{"impl"}})
	res2 := f.spawn("oc2")
	if err := f.launch.Start(*res2); err == nil || !strings.Contains(err.Error(), "XDG_DATA_HOME") {
		t.Fatalf("want XDG_DATA_HOME refusal, got %v", err)
	}
}

func TestErrPathsStartUnboundOrUnknown(t *testing.T) {
	l := launch.New(launch.Options{Exe: "x"})
	defer l.Close()
	if err := l.Start(runs.SpawnResult{RunID: "r1", AccountID: "a"}); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("unbound: %v", err)
	}
	f := newFx(t)
	if err := f.launch.Start(runs.SpawnResult{RunID: "r_nope", AccountID: "s"}); err == nil || !strings.Contains(err.Error(), "unknown run") {
		t.Fatalf("unknown run: %v", err)
	}
	res := f.spawn("s")
	res.AccountID = "ghost"
	if err := f.launch.Start(*res); err == nil || !strings.Contains(err.Error(), "unknown account") {
		t.Fatalf("unknown account: %v", err)
	}
}
