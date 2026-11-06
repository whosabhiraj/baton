// Package gitx runs the git CLI for Baton: every call has a timeout, never prompts
// (GIT_TERMINAL_PROMPT=0) and enables long paths on Windows (worktree paths are long).
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultTimeout bounds every git call made without a deadline of its own.
const DefaultTimeout = 60 * time.Second

// Error is a failed git command with its combined output.
type Error struct {
	Args   []string
	Output string
	Err    error
}

func (e *Error) Error() string {
	out := strings.TrimSpace(e.Output)
	if out == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, out)
}

func (e *Error) Unwrap() error { return e.Err }

// ErrNotFound means the git executable is not on PATH.
var ErrNotFound = errors.New("git not found on PATH")

// Run runs `git -C dir args...` and returns trimmed stdout.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", ErrNotFound
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	full := []string{"-C", dir}
	if runtime.GOOS == "windows" {
		full = append(full, "-c", "core.longpaths=true")
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Args: args, Output: stderr.String() + stdout.String(), Err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// TopLevel returns the repository root containing dir (git rev-parse --show-toplevel),
// as a native, cleaned path.
func TopLevel(dir string) (string, error) {
	out, err := Run(context.Background(), dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.FromSlash(out)), nil
}

// IsRepo reports whether dir is inside a git work tree.
func IsRepo(dir string) bool {
	out, err := Run(context.Background(), dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// Init runs `git init` in dir and creates an initial (possibly empty) commit, so worktrees
// can branch from it. Only call after the user agreed.
func Init(dir string) error {
	if IsUnsafeRoot(dir) {
		return ErrUnsafeRoot
	}
	ctx := context.Background()
	if _, err := Run(ctx, dir, "init"); err != nil {
		return err
	}
	if _, err := Run(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	// Identity fallback only for this commit, so it works on machines without user.name.
	_, err := Run(ctx, dir, "-c", "user.name=baton", "-c", "user.email=baton@localhost",
		"commit", "--allow-empty", "-m", "Initial commit")
	return err
}

// CurrentBranch returns the checked-out branch name, or the HEAD commit id when detached.
func CurrentBranch(dir string) (string, error) {
	ctx := context.Background()
	if b, err := Run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && b != "" {
		return b, nil
	}
	return Run(ctx, dir, "rev-parse", "HEAD")
}

// ErrUnsafeRoot means a git repository was found at (or a command targeted) the user's home
// folder or a filesystem/drive root. Baton never treats those as a project: a stray repo there
// would make every folder under home look like part of one project.
var ErrUnsafeRoot = errors.New("refusing to use the home folder or a filesystem root as a project")

// IsUnsafeRoot reports whether dir is the user's home folder or a filesystem/drive root.
func IsUnsafeRoot(dir string) bool {
	clean := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		p = filepath.Clean(p)
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			p = strings.ToLower(p)
		}
		return p
	}
	d := clean(dir)
	if d == clean(filepath.Dir(d)) { // "/" or "C:\"
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && d == clean(home) {
		return true
	}
	return false
}

// ProjectTopLevel is TopLevel, but returns ErrUnsafeRoot when the repository root is the home
// folder or a filesystem root.
func ProjectTopLevel(dir string) (string, error) {
	top, err := TopLevel(dir)
	if err != nil {
		return "", err
	}
	if IsUnsafeRoot(top) {
		return "", ErrUnsafeRoot
	}
	return top, nil
}
