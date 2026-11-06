package runs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/proto"
)

// DefaultDiffBytes caps the diff text returned by Diff when the caller passes 0.
const DefaultDiffBytes = 200_000

// DiffResult is a run's branch diff against its base (committed work only) plus what is still
// uncommitted in its worktree.
type DiffResult struct {
	RunID       string   `json:"run_id"`
	AgentID     string   `json:"agent_id"`
	Branch      string   `json:"branch"`
	BaseBranch  string   `json:"base_branch"`
	Stat        string   `json:"stat"`
	Diff        string   `json:"diff"`
	Bytes       int      `json:"bytes"` // full diff size before truncation
	Truncated   bool     `json:"truncated"`
	Uncommitted []string `json:"uncommitted"` // `git status --porcelain` lines of the worktree
	Merged      bool     `json:"merged"`      // branch is already contained in base
}

// MergeResult describes a successful merge.
type MergeResult struct {
	RunID         string `json:"run_id"`
	Branch        string `json:"branch"`
	BaseBranch    string `json:"base_branch"`
	Commit        string `json:"commit"`         // base tip after the merge
	AlreadyMerged bool   `json:"already_merged"` // nothing to do
	Method        string `json:"method"`         // "checkout" (merged in the project checkout) | "ref" (base not checked out)
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func gitOutput(err error) string {
	var ge *gitx.Error
	if errors.As(err, &ge) {
		return ge.Output
	}
	return ""
}

func (m *Manager) runBranches(runID string) (rec proto.RunRecord, perr *proto.Error) {
	r := m.o.Store.Get(runID)
	if r == nil {
		return rec, proto.NewError(proto.CodeNotFound, "no such run")
	}
	rec = r.Record()
	if rec.Branch == "" || rec.BaseBranch == "" {
		return rec, proto.NewError(proto.CodeMergeBlocked, "run has no branch (it was not spawned into a worktree)")
	}
	return rec, nil
}

func (m *Manager) uncommitted(ctx context.Context, worktree string) ([]string, error) {
	if worktree == "" {
		return []string{}, nil
	}
	if _, err := os.Stat(worktree); err != nil {
		return []string{}, nil // worktree removed: nothing uncommitted can be lost
	}
	st, err := gitx.Run(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, l := range strings.Split(st, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

func (m *Manager) isAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := gitx.Run(ctx, m.o.Repo, "merge-base", "--is-ancestor", a, b)
	switch {
	case err == nil:
		return true, nil
	case exitCode(err) == 1:
		return false, nil
	}
	return false, err
}

// Diff returns the run's committed changes (base...branch, run in the project repo, so it
// works after the worktree is gone) capped at maxBytes (0 = DefaultDiffBytes).
func (m *Manager) Diff(ctx context.Context, runID string, maxBytes int) (*DiffResult, *proto.Error) {
	rec, perr := m.runBranches(runID)
	if perr != nil {
		return nil, perr
	}
	if maxBytes <= 0 {
		maxBytes = DefaultDiffBytes
	}
	rng := rec.BaseBranch + "..." + rec.Branch
	stat, err := gitx.Run(ctx, m.o.Repo, "diff", "--stat", rng)
	if err != nil {
		return nil, proto.NewError(proto.CodeGit, err.Error())
	}
	diff, err := gitx.Run(ctx, m.o.Repo, "diff", rng)
	if err != nil {
		return nil, proto.NewError(proto.CodeGit, err.Error())
	}
	unc, err := m.uncommitted(ctx, rec.Worktree)
	if err != nil {
		return nil, proto.NewError(proto.CodeGit, err.Error())
	}
	merged, err := m.isAncestor(ctx, rec.Branch, rec.BaseBranch)
	if err != nil {
		return nil, proto.NewError(proto.CodeGit, err.Error())
	}
	res := &DiffResult{RunID: rec.RunID, AgentID: rec.AgentID, Branch: rec.Branch, BaseBranch: rec.BaseBranch,
		Stat: stat, Diff: diff, Bytes: len(diff), Uncommitted: unc, Merged: merged}
	if stat == "" {
		res.Stat = "no changes"
	}
	if len(diff) > maxBytes {
		cut := maxBytes
		for cut > 0 && (diff[cut]&0xC0) == 0x80 { // do not split a UTF-8 sequence
			cut--
		}
		res.Diff, res.Truncated = diff[:cut], true
	}
	return res, nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(filepath.FromSlash(a)), filepath.Clean(filepath.FromSlash(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// checkedOutAt returns the worktree path where branch is checked out ("" = nowhere).
func (m *Manager) checkedOutAt(ctx context.Context, branch string) (string, error) {
	out, err := gitx.Run(ctx, m.o.Repo, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	path := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "worktree "):
			path = strings.TrimPrefix(l, "worktree ")
		case l == "branch refs/heads/"+branch:
			return path, nil
		}
	}
	return "", nil
}

// identity returns `-c user.name/email` fallbacks when the repo has no identity configured
// (same fallback as gitx.Init), so merges work on machines without user.name.
func (m *Manager) identity(ctx context.Context) []string {
	var out []string
	if v, err := gitx.Run(ctx, m.o.Repo, "config", "--get", "user.name"); err != nil || v == "" {
		out = append(out, "-c", "user.name=baton")
	}
	if v, err := gitx.Run(ctx, m.o.Repo, "config", "--get", "user.email"); err != nil || v == "" {
		out = append(out, "-c", "user.email=baton@localhost")
	}
	return out
}

// mergeMessage is the merge commit message: the branch, the callsign and the task's first line.
func mergeMessage(rec proto.RunRecord) string {
	msg := "Merge " + rec.Branch
	if rec.Callsign != "" {
		msg += " (" + rec.Callsign + ")"
	}
	if t := strings.TrimSpace(rec.Task); t != "" {
		first, _, _ := strings.Cut(t, "\n")
		if len(first) > 200 {
			first = first[:200] + "..."
		}
		msg += "\n\n" + first
	}
	return msg + "\n\nRun: " + rec.RunID
}

func conflictErr(files []string) *proto.Error {
	msg := "branch does not merge cleanly into its base"
	if len(files) > 0 {
		msg += "; conflicting files: " + strings.Join(files, ", ")
	}
	return proto.NewError(proto.CodeMergeConflict, msg+". Ask the agent to merge the base branch into its branch and resolve the conflicts.")
}

// Merge merges the run's branch into its base with a merge commit (--no-ff). If the base is
// checked out in the project folder, the merge happens there and only when that checkout has
// no uncommitted changes to tracked files; if the base is not checked out anywhere, the merge
// commit is built with `git merge-tree` and the branch ref is moved. Conflicts are never left
// behind: the merge is refused with MERGE_CONFLICT and nothing changes.
func (m *Manager) Merge(ctx context.Context, runID string) (*MergeResult, *proto.Error) {
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()
	rec, perr := m.runBranches(runID)
	if perr != nil {
		return nil, perr
	}
	res := &MergeResult{RunID: rec.RunID, Branch: rec.Branch, BaseBranch: rec.BaseBranch}
	gitErr := func(err error) *proto.Error { return proto.NewError(proto.CodeGit, err.Error()) }

	if _, err := gitx.Run(ctx, m.o.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+rec.BaseBranch); err != nil {
		return nil, proto.NewError(proto.CodeMergeBlocked, fmt.Sprintf("base %q is not a local branch", rec.BaseBranch))
	}
	if _, err := gitx.Run(ctx, m.o.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+rec.Branch); err != nil {
		return nil, proto.NewError(proto.CodeMergeBlocked, fmt.Sprintf("agent branch %q does not exist", rec.Branch))
	}
	unc, err := m.uncommitted(ctx, rec.Worktree)
	if err != nil {
		return nil, gitErr(err)
	}
	if len(unc) > 0 {
		return nil, proto.NewError(proto.CodeMergeBlocked, fmt.Sprintf(
			"the agent's worktree has %d uncommitted change(s); ask the agent to commit them first", len(unc)))
	}
	merged, err := m.isAncestor(ctx, rec.Branch, rec.BaseBranch)
	if err != nil {
		return nil, gitErr(err)
	}
	if merged {
		res.AlreadyMerged = true
		res.Commit, _ = gitx.Run(ctx, m.o.Repo, "rev-parse", "refs/heads/"+rec.BaseBranch)
		return res, nil
	}
	at, err := m.checkedOutAt(ctx, rec.BaseBranch)
	if err != nil {
		return nil, gitErr(err)
	}
	msg := mergeMessage(rec)
	switch {
	case at == "":
		res.Method = "ref"
		return m.mergeRef(ctx, rec, msg, res)
	case !samePath(at, m.o.Repo):
		return nil, proto.NewError(proto.CodeMergeBlocked, fmt.Sprintf("base %q is checked out in another worktree (%s)", rec.BaseBranch, at))
	}
	res.Method = "checkout"
	dirty, err := gitx.Run(ctx, m.o.Repo, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, gitErr(err)
	}
	if dirty != "" {
		return nil, proto.NewError(proto.CodeMergeBlocked, fmt.Sprintf(
			"your checkout of %q has uncommitted changes; commit or stash them, then approve the merge again", rec.BaseBranch))
	}
	args := append(m.identity(ctx), "merge", "--no-ff", "--no-edit", "-m", msg, rec.Branch)
	if _, err := gitx.Run(ctx, m.o.Repo, args...); err != nil {
		files, _ := gitx.Run(ctx, m.o.Repo, "diff", "--name-only", "--diff-filter=U")
		_, _ = gitx.Run(ctx, m.o.Repo, "merge", "--abort")
		if files != "" {
			return nil, conflictErr(strings.Split(files, "\n"))
		}
		out := gitOutput(err)
		if strings.Contains(out, "would be overwritten") {
			return nil, proto.NewError(proto.CodeMergeBlocked, "files in your checkout would be overwritten by the merge: "+strings.TrimSpace(out))
		}
		return nil, gitErr(err)
	}
	if res.Commit, err = gitx.Run(ctx, m.o.Repo, "rev-parse", "HEAD"); err != nil {
		return nil, gitErr(err)
	}
	return res, nil
}

// mergeRef merges without a checkout: merge-tree computes the tree, commit-tree records the
// merge commit, update-ref moves the base (only if it did not move meanwhile).
func (m *Manager) mergeRef(ctx context.Context, rec proto.RunRecord, msg string, res *MergeResult) (*MergeResult, *proto.Error) {
	old, err := gitx.Run(ctx, m.o.Repo, "rev-parse", "refs/heads/"+rec.BaseBranch)
	if err != nil {
		return nil, proto.NewError(proto.CodeGit, err.Error())
	}
	out, err := gitx.Run(ctx, m.o.Repo, "merge-tree", "--write-tree", "--name-only", "--no-messages", old, "refs/heads/"+rec.Branch)
	if err != nil {
		if exitCode(err) == 1 {
			lines := strings.Split(strings.TrimSpace(gitOutput(err)), "\n")
			files := []string{}
			for _, l := range lines[1:] { // first line is the tree id
				if l = strings.TrimSpace(l); l != "" {
					files = append(files, l)
				}
			}
			return nil, conflictErr(files)
		}
		return nil, proto.NewError(proto.CodeGit, err.Error())
	}
	tree, _, _ := strings.Cut(out, "\n")
	args := append(m.identity(ctx), "commit-tree", strings.TrimSpace(tree), "-p", old, "-p", "refs/heads/"+rec.Branch, "-m", msg)
	commit, err := gitx.Run(ctx, m.o.Repo, args...)
	if err != nil {
		return nil, proto.NewError(proto.CodeGit, err.Error())
	}
	if _, err := gitx.Run(ctx, m.o.Repo, "update-ref", "-m", "baton: "+strings.SplitN(msg, "\n", 2)[0],
		"refs/heads/"+rec.BaseBranch, commit, old); err != nil {
		return nil, proto.NewError(proto.CodeGit, err.Error())
	}
	res.Commit = commit
	return res, nil
}
