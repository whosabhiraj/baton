package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/daemon"
)

type fakeDaemon struct {
	state   daemon.State
	info    *daemon.Info
	started [][]string
	stopped int
	opened  []string
}

func stubDaemon(t *testing.T, f *fakeDaemon, openBrowserCfg bool) {
	oD, oS, oW, oT, oE, oB, oC := daemonDiscover, daemonStart, daemonWait, daemonStop, executable, openBrowser, loadConfig
	t.Cleanup(func() {
		daemonDiscover, daemonStart, daemonWait, daemonStop, executable, openBrowser, loadConfig = oD, oS, oW, oT, oE, oB, oC
	})
	daemonDiscover = func(string) (*daemon.Info, daemon.State, error) { return f.info, f.state, nil }
	daemonStart = func(exe string, args []string, _ string) error {
		f.started = append(f.started, args)
		f.state = daemon.Running
		return nil
	}
	daemonWait = func(string, time.Duration) (*daemon.Info, error) { return f.info, nil }
	daemonStop = func(string, string, time.Duration) error { f.stopped++; f.state = daemon.Stopped; return nil }
	executable = func() (string, error) { return "baton-test", nil }
	openBrowser = func(u string) { f.opened = append(f.opened, u) }
	loadConfig = func(string) *config.Snapshot {
		return &config.Snapshot{Config: config.Config{Hub: config.Hub{Port: 7777, OpenBrowser: openBrowserCfg}}}
	}
}

func runMain(stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(stdin), &out, &errb, func(string) string { return "" })
	return code, out.String(), errb.String()
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if b, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, b)
	}
	return dir
}

func TestUpStartsAndOpens(t *testing.T) {
	f := &fakeDaemon{state: daemon.Stopped, info: &daemon.Info{URL: "http://127.0.0.1:7777", PID: 5}}
	stubDaemon(t, f, true)
	t.Setenv("BATON_HOME", t.TempDir())
	t.Chdir(gitRepo(t))

	code, out, _ := runMain("", "up")
	if code != 0 || len(f.started) != 1 || f.started[0][0] != "hub" || f.started[0][1] != "serve" {
		t.Fatalf("code %d started %v", code, f.started)
	}
	if !strings.HasPrefix(out, "baton hub: http://127.0.0.1:7777\ndashboard: http://127.0.0.1:7777/\nadmin token:") || strings.Contains(out, "?token") || strings.Contains(out, "conductor") {
		t.Fatalf("out %q", out)
	}
	if len(f.opened) != 1 || f.opened[0] != "http://127.0.0.1:7777/" {
		t.Fatalf("opened %v", f.opened)
	}
	// already running: no second start; --no-browser: no open; bare baton adds conductor line
	code, out, _ = runMain("", "--no-browser")
	if code != 0 || len(f.started) != 1 || len(f.opened) != 1 || !strings.Contains(out, "conductor: arrives in Phase 6") {
		t.Fatalf("code %d started %v opened %v out %q", code, f.started, f.opened, out)
	}
}

func TestUpNotARepo(t *testing.T) {
	f := &fakeDaemon{state: daemon.Running, info: &daemon.Info{URL: "http://x"}}
	stubDaemon(t, f, false)
	t.Setenv("BATON_HOME", t.TempDir())
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	dir := t.TempDir()
	t.Chdir(dir)
	for _, in := range []string{"", "n\n", "\n"} {
		code, _, e := runMain(in, "up")
		if code != 2 || !strings.Contains(e, "is not a git repository. Initialise one here? [y/N] ") {
			t.Fatalf("%q: code %d err %q", in, code, e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Fatal("repo created without consent")
	}
	if code, _, e := runMain("y\n", "up"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatal("repo not created")
	}
}

func TestHubStopAsksWhenRunsLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"runs":[{"status":"running"},{"status":"done"},{"status":"stalled"}]}`))
	}))
	defer srv.Close()
	f := &fakeDaemon{state: daemon.Running, info: &daemon.Info{URL: srv.URL}}
	stubDaemon(t, f, false)
	dd := t.TempDir()

	code, _, e := runMain("", "hub", "stop", "--data-dir", dd)
	if code != 1 || !strings.Contains(e, "2 agent runs are live. Stop the hub? [y/N] ") || f.stopped != 0 {
		t.Fatalf("code %d err %q stopped %d", code, e, f.stopped)
	}
	if code, out, _ := runMain("y\n", "hub", "stop", "--data-dir", dd); code != 0 || f.stopped != 1 || !strings.Contains(out, "stopped") {
		t.Fatalf("code %d out %q stopped %d", code, out, f.stopped)
	}
	f.state = daemon.Running
	if code, _, _ := runMain("", "hub", "stop", "--yes", "--keep-agents", "--data-dir", dd); code != 0 || f.stopped != 2 {
		t.Fatalf("code %d stopped %d", code, f.stopped)
	}
}

func TestRotateToken(t *testing.T) {
	f := &fakeDaemon{state: daemon.Running, info: &daemon.Info{URL: "http://x"}}
	stubDaemon(t, f, false)
	dd := t.TempDir()
	if err := os.WriteFile(filepath.Join(dd, "admin.token"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, e := runMain("", "hub", "rotate-token", "--data-dir", dd)
	if code != 0 {
		t.Fatal(code, e)
	}
	b, _ := os.ReadFile(filepath.Join(dd, "admin.token"))
	tok := strings.TrimSpace(string(b))
	if tok == "old" || !strings.HasPrefix(tok, "bt_adm_") || out != "admin token: "+tok+"\n" {
		t.Fatalf("tok %q out %q", tok, out)
	}
	if f.stopped != 1 || len(f.started) != 1 {
		t.Fatalf("stopped %d started %v", f.stopped, f.started)
	}
}

// Windows PowerShell 5.1 pipes `"y" | baton ...` as BOM + "y\r\n"; that must count as yes.
func TestAskAcceptsBOMPrefixedYes(t *testing.T) {
	var errb bytes.Buffer
	c := &ctx{stdin: strings.NewReader(string(rune(0xFEFF)) + "y\r\n"), stderr: &errb}
	if !c.ask("ok? ") {
		t.Fatal("BOM-prefixed y was read as no")
	}
}
