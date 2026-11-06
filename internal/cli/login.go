package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/proc"
)

// loginRun runs an external CLI attached to the terminal and returns its exit code. A seam
// so tests never start real CLIs.
var loginRun = func(argv, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, stdin, stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

var loginKinds = map[string]struct {
	isoVar string
	login  []string
	status []string
}{
	"claude":   {"CLAUDE_CONFIG_DIR", []string{"claude", "auth", "login"}, []string{"claude", "auth", "status"}},
	"codex":    {"CODEX_HOME", []string{"codex", "login"}, []string{"codex", "login", "status"}},
	"opencode": {"OPENCODE_CONFIG_DIR", []string{"opencode", "auth", "login"}, []string{"opencode", "auth", "list"}},
}

// cmdLogin: baton login <account-id> | baton login --status [<account-id>].
// --status takes no value here (unlike `done --status`), so it is pulled out before parsing.
func (c *ctx) cmdLogin(argv []string) (int, error) {
	status := false
	var rest []string
	for _, a := range argv {
		if a == "--status" {
			status = true
		} else {
			rest = append(rest, a)
		}
	}
	o, pos, err := parseArgs(rest)
	if err != nil {
		return 0, usagef("%s", err.Error())
	}
	if len(pos) > 1 {
		return 0, usagef("unexpected argument %q", pos[1])
	}
	repo, _, err := project(o)
	if err != nil {
		return 0, err
	}
	cfg := loadConfig(repo).Config
	if len(pos) == 0 {
		if !status {
			return 0, usagef("missing account id")
		}
		worst := 0
		for _, a := range cfg.Accounts {
			fmt.Fprintf(c.stdout, "== %s (%s)\n", a.ID, a.Kind)
			code, err := c.loginOne(a, true)
			if err != nil {
				fmt.Fprintf(c.stderr, "baton: %s\n", err)
				code = 1
			}
			if code > worst {
				worst = code
			}
		}
		return worst, nil
	}
	a, ok := cfg.Account(pos[0])
	if !ok {
		return 0, fmt.Errorf("unknown account %q", pos[0])
	}
	return c.loginOne(*a, status)
}

func (c *ctx) loginOne(a config.Account, status bool) (int, error) {
	k, ok := loginKinds[a.Kind]
	if !ok {
		fmt.Fprintln(c.stdout, "no login for custom kinds")
		return 0, nil
	}
	env := proc.BuildEnv(nil, a.Env, nil)
	dir := ""
	for _, kv := range env {
		if name, v, _ := strings.Cut(kv, "="); strings.EqualFold(name, k.isoVar) {
			dir = v
		}
	}
	if dir == "" {
		return 0, fmt.Errorf("account %q has no %s in its env; set it (for example %s: ~/.baton/%s) so the login does not land in your normal %s folder",
			a.ID, k.isoVar, k.isoVar, a.ID, a.Kind)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	fmt.Fprintf(c.stdout, "%s=%s\n", k.isoVar, dir)
	argv := k.login
	if status {
		argv = k.status
	}
	code, err := loginRun(argv, env, c.stdin, c.stdout, c.stderr)
	if err != nil {
		return 0, fmt.Errorf("cannot run %s: %w", argv[0], err)
	}
	return code, nil
}
