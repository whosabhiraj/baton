package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/paths"
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
	"opencode": {"XDG_DATA_HOME", []string{"opencode", "auth", "login"}, []string{"opencode", "auth", "list"}},
}

// cmdLogin: baton login <account-id> [--kind K] | baton login --status [<account-id>].
// An unknown id (without --status) is added to the global config first.
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
	snap := loadConfig(repo)
	cfg := snap.Config
	if o.has("kind") && (status || len(pos) == 0) {
		return 0, usagef("--kind only works when adding a new account")
	}
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
	if !ok && status {
		return 0, fmt.Errorf("unknown account %q", pos[0])
	}
	if !ok {
		added, err := c.addAccount(snap, repo, pos[0], o.get("kind"))
		if err != nil || added == nil {
			return 2, err
		}
		a = added
	} else if o.has("kind") {
		return 0, usagef("account %q already exists (kind %s); --kind only works when adding a new account", a.ID, a.Kind)
	}
	return c.loginOne(*a, status)
}

// addAccount adds a new account to the global config (asking for the kind unless --kind is
// given), with its own login folder under ~/.baton/accounts/<id>. nil, nil = cancelled.
func (c *ctx) addAccount(snap *config.Snapshot, repo, id, kind string) (*config.Account, error) {
	gp := snap.GlobalPath
	if gp == "" {
		gp = config.GlobalPath()
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		fmt.Fprintf(c.stderr, "account %q is not in %s. Add it? Kind [claude/codex], empty = cancel: ", id, gp)
		line, _ := bufio.NewReader(c.stdin).ReadString('\n')
		kind = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, string(rune(0xFEFF)))))
		if kind == "" {
			fmt.Fprintln(c.stderr, "cancelled")
			return nil, nil
		}
	}
	na := config.NewAccount{ID: id, Label: id, Kind: kind, MaxConcurrent: 2}
	switch kind {
	case "claude":
		na.Models = []string{"sonnet", "opus", "haiku"}
	case "codex":
		na.Models = []string{"default"}
	default:
		return nil, fmt.Errorf("kind must be claude or codex (add other kinds by editing %s)", gp)
	}
	na.Env = map[string]string{loginKinds[kind].isoVar: accountDir(id)}
	if err := config.AddAccount(snap, na); err != nil {
		return nil, err
	}
	fmt.Fprintf(c.stdout, "added account %s (%s) to %s; edit it there for label, colour, models, roles\n", id, kind, gp)
	a, ok := loadConfig(repo).Config.Account(id)
	if !ok {
		return nil, fmt.Errorf("account %q was written to %s but did not load back", id, gp)
	}
	return a, nil
}

// accountDir is the login folder for a new account, written with ~ when it sits under the
// default ~/.baton so the config stays portable.
func accountDir(id string) string {
	dir := filepath.Join(paths.BatonHome(), "accounts", id)
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, dir); err == nil && rel == filepath.Join(".baton", "accounts", id) {
			return "~/.baton/accounts/" + id
		}
	}
	return dir
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
