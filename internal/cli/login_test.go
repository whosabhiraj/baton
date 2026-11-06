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

	if code, _, errs := run("nope"); code != 1 || !strings.Contains(errs, "unknown account") {
		t.Errorf("unknown: %d %q", code, errs)
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
