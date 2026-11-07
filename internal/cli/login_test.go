package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/config"
)

type loginCall struct {
	argv, env []string
}

func loginHarness(t *testing.T, accts []config.Account) (*[]loginCall, func(args ...string) (int, string, string)) {
	t.Helper()
	oldCfg, oldRun := loadConfig, loginRun
	t.Cleanup(func() { loadConfig, loginRun = oldCfg, oldRun })
	loadConfig = func(string) *config.Snapshot {
		return &config.Snapshot{Config: config.Config{Accounts: accts}}
	}
	var calls []loginCall
	loginRun = func(argv, env []string, _ io.Reader, _, _ io.Writer) (int, error) {
		calls = append(calls, loginCall{argv, env})
		return 7, nil
	}
	return &calls, func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		args = append([]string{"login"}, args...)
		args = append(args, "--data-dir", t.TempDir())
		code := Main(args, strings.NewReader(""), &out, &errb, func(string) string { return "" })
		return code, out.String(), errb.String()
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestLogin(t *testing.T) {
	root := t.TempDir()
	cdir, xdir := filepath.Join(root, "c"), filepath.Join(root, "x")
	calls, run := loginHarness(t, []config.Account{
		{ID: "c1", Kind: "claude", Env: map[string]string{"CLAUDE_CONFIG_DIR": cdir}},
		{ID: "x1", Kind: "codex", Env: map[string]string{"CODEX_HOME": xdir}},
		{ID: "bare", Kind: "claude"},
		{ID: "bare2", Kind: "codex", Env: map[string]string{"OTHER": "1"}},
		{ID: "cust", Kind: "mine"},
	})

	if code, _, errs := run("nope"); code != 2 || !strings.Contains(errs, "cancelled") || len(*calls) != 0 {
		t.Errorf("unknown, empty answer: %d %q", code, errs)
	}
	if code, _, errs := run("nope", "--status"); code != 1 || !strings.Contains(errs, "unknown account") {
		t.Errorf("unknown status: %d %q", code, errs)
	}
	if code, _, _ := run("c1", "--kind", "claude"); code != 2 || len(*calls) != 0 {
		t.Errorf("--kind on existing account: %d", code)
	}
	for _, id := range []string{"bare", "bare2"} {
		code, _, errs := run(id)
		if code != 1 || !strings.Contains(errs, "has no ") || len(*calls) != 0 {
			t.Errorf("%s refusal: %d %q calls=%d", id, code, errs, len(*calls))
		}
	}
	if code, out, _ := run("cust"); code != 0 || !strings.Contains(out, "no login for custom kinds") || len(*calls) != 0 {
		t.Errorf("custom: %d %q", code, out)
	}

	cases := []struct {
		args []string
		argv string
		env  string
		dir  string
	}{
		{[]string{"c1"}, "claude auth login", "CLAUDE_CONFIG_DIR=" + cdir, cdir},
		{[]string{"c1", "--status"}, "claude auth status", "CLAUDE_CONFIG_DIR=" + cdir, cdir},
		{[]string{"--status", "x1"}, "codex login status", "CODEX_HOME=" + xdir, xdir},
		{[]string{"x1"}, "codex login", "CODEX_HOME=" + xdir, xdir},
	}
	for _, c := range cases {
		*calls = nil
		code, out, _ := run(c.args...)
		if code != 7 || len(*calls) != 1 {
			t.Fatalf("%v: code %d calls %d", c.args, code, len(*calls))
		}
		got := (*calls)[0]
		if strings.Join(got.argv, " ") != c.argv || !hasEnv(got.env, c.env) {
			t.Errorf("%v: argv %q env ok=%v", c.args, got.argv, hasEnv(got.env, c.env))
		}
		if !strings.Contains(out, c.env) {
			t.Errorf("%v: dir not printed: %q", c.args, out)
		}
		if st, err := os.Stat(c.dir); err != nil || !st.IsDir() {
			t.Errorf("dir not created: %v", err)
		}
	}
}

func TestLoginStatusAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c")
	calls, run := loginHarness(t, []config.Account{
		{ID: "c1", Kind: "claude", Env: map[string]string{"CLAUDE_CONFIG_DIR": dir}},
		{ID: "bare", Kind: "claude"},
	})
	code, out, errs := run("--status")
	if code != 7 || len(*calls) != 1 || !strings.Contains(out, "== c1") || !strings.Contains(errs, "bare") {
		t.Errorf("status all: %d %q %q calls=%d", code, out, errs, len(*calls))
	}
	if code, _, _ := run(); code != 2 {
		t.Errorf("missing id: %d", code)
	}
}

// TestLoginAddsAccount: an unknown id is added to the real global config (asked kind or
// --kind), then logged in with its own folder; bad kinds and ids are refused.
func TestLoginAddsAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BATON_HOME", home)
	gp := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(gp, []byte("# keep me\nhub:\n  mode: propose\naccounts:\n  - id: old\n    kind: claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCfg, oldRun := loadConfig, loginRun
	t.Cleanup(func() { loadConfig, loginRun = oldCfg, oldRun })
	loadConfig = func(string) *config.Snapshot { return config.Load(gp, "") }
	var calls []loginCall
	loginRun = func(argv, env []string, _ io.Reader, _, _ io.Writer) (int, error) {
		calls = append(calls, loginCall{argv, env})
		return 0, nil
	}
	run := func(stdin string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		args = append(append([]string{"login"}, args...), "--data-dir", t.TempDir())
		code := Main(args, strings.NewReader(stdin), &out, &errb, func(string) string { return "" })
		return code, out.String(), errb.String()
	}

	if code, out, errs := run("Claude\n", "new-one"); code != 0 || !strings.Contains(out, "added account new-one") {
		t.Fatalf("prompted add: %d %q %q", code, out, errs)
	}
	if code, _, errs := run("", "cx", "--kind", "codex"); code != 0 {
		t.Fatalf("--kind add: %d %q", code, errs)
	}
	if len(calls) != 2 || strings.Join(calls[0].argv, " ") != "claude auth login" || strings.Join(calls[1].argv, " ") != "codex login" {
		t.Fatalf("login calls: %+v", calls)
	}
	cdir := filepath.Join(home, "accounts", "new-one")
	if !hasEnv(calls[0].env, "CLAUDE_CONFIG_DIR="+cdir) {
		t.Errorf("claude dir not isolated: %v", calls[0].env)
	}
	if st, err := os.Stat(cdir); err != nil || !st.IsDir() {
		t.Errorf("account dir not created: %v", err)
	}
	snap := config.Load(gp, "")
	if !snap.OK() {
		t.Fatalf("config invalid after add: %+v", snap.Issues)
	}
	a, ok := snap.Config.Account("new-one")
	if !ok || a.Kind != "claude" || a.Models[0] != "sonnet" || a.MaxConcurrent != 2 || !a.Enabled {
		t.Errorf("new-one: %+v", a)
	}
	if x, ok := snap.Config.Account("cx"); !ok || x.Kind != "codex" {
		t.Errorf("cx: %+v", x)
	}
	if data, _ := os.ReadFile(gp); !strings.Contains(string(data), "# keep me") {
		t.Errorf("comment lost:\n%s", data)
	}

	before, _ := os.ReadFile(gp)
	for _, c := range []struct{ stdin, id string }{{"opencode\n", "oc"}, {"claude\n", "Bad_ID"}} {
		if code, _, errs := run(c.stdin, c.id); code != 1 || errs == "" {
			t.Errorf("%s refused: %d %q", c.id, code, errs)
		}
	}
	if after, _ := os.ReadFile(gp); string(after) != string(before) {
		t.Errorf("refused add changed the config")
	}
	if len(calls) != 2 {
		t.Errorf("refused add still logged in: %d calls", len(calls))
	}
}
