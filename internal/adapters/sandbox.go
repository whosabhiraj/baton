package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/whosabhiraj/baton/internal/config"
)

// Sandbox describes what a sandboxed agent may touch. The worktree (its cwd) and RunDir are
// writable, GitWrite are the few paths of the main repo's .git a commit on the agent's own
// branch needs; BatonHome and DenyRead are not readable; there is no network.
type Sandbox struct {
	Worktree  string
	RunDir    string
	GitWrite  []string
	BatonHome string
	DenyRead  []string
	GOOS      string
	Driver    string // the baton binary (runs `baton __codex`, `baton __permit`)
	HubURL    string // for drivers that file access requests (never put in the agent's own env)
	HubToken  string
	RunID     string
	// PrivateDir is a folder the agent cannot read (inside Baton's home, outside its worktree and
	// run dir), for files with the run token (claude's MCP config). "" = claude cannot ask.
	PrivateDir  string
	AskTimeoutS float64 // how long an access request waits for the user
}

// askVia is how a sandboxed agent's prompts reach the user.
func (sb Sandbox) askVia() AskVia {
	return AskVia{Driver: sb.Driver, HubURL: sb.HubURL, HubToken: sb.HubToken, RunID: sb.RunID,
		PrivateDir: sb.PrivateDir, TimeoutS: sb.AskTimeoutS}
}

// SandboxPlan is how to start a sandboxed agent: argv, files to write into the run dir, files
// to write into the private dir (Sandbox.PrivateDir, mode 0600), and extra environment for the
// process Baton starts (a driver strips its secrets before starting the CLI; for a CLI started
// directly it holds nothing secret).
type SandboxPlan struct {
	Argv    []string
	Files   map[string][]byte
	Private map[string][]byte
	Env     map[string]string
}

// ErrNoSandbox: this kind cannot be sandboxed on this platform.
var ErrNoSandbox = errors.New("cannot be sandboxed here")

// Sandboxer is implemented by adapters that can run their agent inside the CLI's OS sandbox
// (permissions.mode: sandboxed). Files are written into RunDir before the agent starts.
type Sandboxer interface {
	SandboxArgv(acct config.Account, model, effort string, sb Sandbox) (SandboxPlan, error)
}

func (sb Sandbox) denied() []string {
	return append([]string{sb.BatonHome}, sb.DenyRead...)
}

// ---- claude ----

// ClaudeSandboxFile is the --settings file Baton writes into the run dir.
const ClaudeSandboxFile = "claude-sandbox.json"

func (c claude) SandboxArgv(acct config.Account, model, effort string, sb Sandbox) (SandboxPlan, error) {
	if sb.GOOS == "windows" {
		return SandboxPlan{}, fmt.Errorf("claude %w: Claude Code's sandbox needs macOS, Linux or WSL2; set permissions.mode: permissive on account %s to run it unsandboxed", ErrNoSandbox, acct.ID)
	}
	write := append([]string{sb.Worktree, sb.RunDir}, sb.GitWrite...)
	// Permission rules cannot re-allow inside a deny (deny always wins), and the worktree lives
	// inside Baton's home, so the Read/Edit tools get every sibling on the way down instead.
	var deny []string
	for _, p := range append(DenyAround(sb.BatonHome, []string{sb.Worktree, sb.RunDir}), sb.DenyRead...) {
		p = filepath.ToSlash(p)
		deny = append(deny, "Read(/"+p+"/**)", "Edit(/"+p+"/**)") // "//abs/path" in permission rules
	}
	// With permissions.ask, what the sandbox blocks becomes a permission prompt that
	// `baton __permit` turns into an access request: a command may ask to run outside the
	// sandbox (that one command then runs with the user's full access), a new network host asks
	// once and stays allowed for the run. Without ask, both are simply refused.
	ask := acct.Permissions.Ask && sb.askVia().ok()
	settings := map[string]any{
		"sandbox": map[string]any{
			"enabled":           true,
			"failIfUnavailable": true, // never fall back to running unsandboxed
			// sandboxed commands run without a prompt (claude's default is to prompt since 2.1.324,
			// which a headless run can only deny)
			"autoAllowBashIfSandboxed": true,
			"allowUnsandboxedCommands": ask,
			"filesystem": map[string]any{
				"denyRead":   sb.denied(),
				"allowRead":  []string{sb.Worktree, sb.RunDir}, // inside Baton's home: its code, its spool and inbox
				"allowWrite": write,
			},
			"network": map[string]any{"allowedDomains": []string{}, "strictAllowlist": !ask},
		},
		"permissions": map[string]any{"deny": deny},
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return SandboxPlan{}, err
	}
	argv := claudeBase(model, effort)
	argv = append(argv, "--permission-mode", "acceptEdits", "--settings", filepath.Join(sb.RunDir, ClaudeSandboxFile))
	plan := SandboxPlan{Files: map[string][]byte{ClaudeSandboxFile: b}}
	if ask {
		pp, err := claude{}.PromptPlan(sb.askVia())
		if err != nil {
			return SandboxPlan{}, err
		}
		argv = append(argv, pp.Argv...)
		plan.Private, plan.Env = pp.Private, pp.Env
	} else {
		argv = append(argv, "--permission-prompts", "none") // nobody to ask: anything that would prompt is refused
	}
	argv = append(argv, claudeToolFlags(acct.Permissions)...)
	plan.Argv = append(argv, acct.Permissions.ExtraArgs...)
	return plan, nil
}

// ---- codex ----

// SandboxArgv runs codex with a named permissions profile: read everything but Baton's home
// (and deny_read), write the worktree, the run dir and the git paths, no network. With
// permissions.ask and a driver, blocked actions (network hosts included) become access
// requests; otherwise it never asks (a blocked action fails). On Windows it uses the mxc
// sandbox (deny-read needs it).
func (codex) SandboxArgv(acct config.Account, model, effort string, sb Sandbox) (SandboxPlan, error) {
	p := acct.Permissions
	if len(p.AllowedTools) > 0 || len(p.DeniedTools) > 0 {
		return SandboxPlan{}, errors.New("allowed_tools/denied_tools are claude-only")
	}
	entries := []string{`":root"="read"`, `":workspace_roots"="write"`}
	add := func(path, access string) error {
		if strings.ContainsAny(path, "'\n\r") {
			return fmt.Errorf("codex sandbox: path %q cannot be used (contains a quote or newline)", path)
		}
		entries = append(entries, "'"+path+"'=\""+access+"\"")
		return nil
	}
	for _, w := range append([]string{sb.RunDir}, sb.GitWrite...) {
		if err := add(w, "write"); err != nil {
			return SandboxPlan{}, err
		}
	}
	// Deny the siblings on the way down to the worktree and run dir rather than Baton's whole
	// home: under codex exec's shell a command cannot start inside a denied parent (it ends up
	// in C:\ on Windows).
	for _, d := range append(DenyAround(sb.BatonHome, []string{sb.Worktree, sb.RunDir}), sb.DenyRead...) {
		if err := add(d, "deny"); err != nil {
			return SandboxPlan{}, err
		}
	}
	var cfg []string // -c overrides, the same for exec and the app-server driver
	if sb.GOOS == "windows" {
		cfg = append(cfg, "-c", `windows.sandbox="mxc"`)
	}
	cfg = append(cfg, "-c", `default_permissions="baton"`,
		"-c", "permissions.baton.filesystem={"+strings.Join(entries, ", ")+"}")
	if p.Ask && sb.Driver != "" {
		// baton __codex runs codex app-server and turns its approval requests into access
		// requests the user decides in the dashboard (A20).
		argv := []string{sb.Driver, "__codex"}
		if model != "" && model != "default" {
			argv = append(argv, "-c", "model="+tomlString(model))
		}
		if e := codexEffort(effort); e != "" {
			argv = append(argv, "-c", "model_reasoning_effort="+tomlString(e))
		}
		// A blocked command may ask for extra permissions (network, paths) with its approval, or
		// through the request_permissions tool; both become access requests. Both features are
		// experimental in codex. The profile itself has no network section: no network.
		argv = append(append(argv, cfg...), "-c", "features.exec_permission_approvals=true", "-c", "features.request_permissions_tool=true")
		argv = append(argv, p.ExtraArgs...)
		return SandboxPlan{Argv: argv, Env: map[string]string{"BATON_DRIVER_URL": sb.HubURL, "BATON_DRIVER_TOKEN": sb.HubToken}}, nil
	}
	argv := append(codexBase(model, effort), cfg...)
	argv = append(argv, "-c", `approval_policy="never"`) // nobody to ask: a blocked action fails
	argv = append(argv, p.ExtraArgs...)
	return SandboxPlan{Argv: append(argv, "-")}, nil // brief on stdin
}

func tomlString(s string) string { b, _ := json.Marshal(s); return string(b) }

// DenyAround lists the entries under root that are not on the way to any of keep: walking from
// root down to each kept path, every sibling at every level. Entries created later are not
// covered, which is fine for Baton's home (new runs land in runs/, which is listed).
func DenyAround(root string, keep []string) []string {
	root = filepath.Clean(root)
	onPath := map[string]bool{}
	for _, k := range keep {
		for p := filepath.Clean(k); ; p = filepath.Dir(p) {
			onPath[pathKey(p)] = true
			if len(p) <= len(root) || filepath.Dir(p) == p {
				break
			}
		}
	}
	var out []string
	var walk func(dir string)
	walk = func(dir string) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			key := pathKey(p)
			switch {
			case !onPath[key]:
				out = append(out, p)
			case e.IsDir() && !isKept(key, keep):
				walk(p)
			}
		}
	}
	if !onPath[pathKey(root)] {
		return []string{root} // nothing to keep inside: deny it whole
	}
	walk(root)
	return out
}

func isKept(key string, keep []string) bool {
	for _, k := range keep {
		if pathKey(filepath.Clean(k)) == key {
			return true
		}
	}
	return false
}

// pathKey compares paths case-insensitively where the file system usually is.
func pathKey(p string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(p)
	}
	return p
}
