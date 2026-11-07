package runs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/runs"
)

func gitIdentity(t *testing.T) {
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "t")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t@t")
	}
}

// agentCommit spawns a run and commits name=content in its worktree.
func agentCommit(t *testing.T, f *fixture, name, content string) *runs.SpawnResult {
	t.Helper()
	res, e := f.m.Spawn(context.Background(), req("claude-b"))
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(res.Worktree, name), []byte(content), 0o644)
	git(t, res.Worktree, "add", name)
	git(t, res.Worktree, "commit", "-m", "agent work")
	return res
}

func parents(t *testing.T, dir, rev string) int {
	return len(strings.Fields(git(t, dir, "rev-list", "--parents", "-n", "1", rev))) - 1
}

func TestDiff(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	ctx := context.Background()
	res := agentCommit(t, f, "b.txt", strings.Repeat("line\n", 100))
	os.WriteFile(filepath.Join(res.Worktree, "c.txt"), []byte("z\n"), 0o644)
	d, e := f.m.Diff(ctx, res.RunID, 0)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(d.Diff, "+++ b/b.txt") || !strings.Contains(d.Stat, "b.txt") || d.Truncated || d.Merged ||
		len(d.Uncommitted) != 1 || !strings.Contains(d.Uncommitted[0], "c.txt") || d.BaseBranch != "main" {
		t.Fatalf("%+v", d)
	}
	small, _ := f.m.Diff(ctx, res.RunID, 50)
	if !small.Truncated || len(small.Diff) > 50 || small.Bytes != d.Bytes {
		t.Fatalf("%+v", small)
	}
	if _, e := f.m.Diff(ctx, "r_nope", 0); e == nil || e.Code != "NOT_FOUND" {
		t.Fatal(e)
	}
}

func TestMergeInCheckout(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	ctx := context.Background()
	res := agentCommit(t, f, "b.txt", "b\n")
	endRun(t, f, res.RunID)

	// dirty tracked file in the user's checkout blocks; an untracked one does not
	os.WriteFile(filepath.Join(f.repo, "a.txt"), []byte("mine\n"), 0o644)
	_, e := f.m.Merge(ctx, res.RunID)
	wantCode(t, e, "MERGE_BLOCKED")
	if !strings.Contains(e.Msg, "uncommitted changes") {
		t.Fatal(e.Msg)
	}
	git(t, f.repo, "checkout", "--", "a.txt")
	os.WriteFile(filepath.Join(f.repo, "notes.txt"), []byte("untracked\n"), 0o644)

	m, e := f.m.Merge(ctx, res.RunID)
	if e != nil {
		t.Fatal(e)
	}
	if m.Method != "checkout" || m.AlreadyMerged || m.Commit != git(t, f.repo, "rev-parse", "HEAD") {
		t.Fatalf("%+v", m)
	}
	if parents(t, f.repo, "HEAD") != 2 {
		t.Fatal("not a merge commit")
	}
	if b, _ := os.ReadFile(filepath.Join(f.repo, "b.txt")); strings.TrimSpace(string(b)) != "b" { // autocrlf may add \r
		t.Fatal("merged file missing in checkout")
	}
	if msg := git(t, f.repo, "log", "-1", "--format=%B"); !strings.Contains(msg, "Merge "+res.Branch) || !strings.Contains(msg, "do the thing") {
		t.Fatal(msg)
	}
	again, e := f.m.Merge(ctx, res.RunID)
	if e != nil || !again.AlreadyMerged {
		t.Fatal(again, e)
	}
	if d, _ := f.m.Diff(ctx, res.RunID, 0); !d.Merged {
		t.Fatal("diff should report merged")
	}
}

func TestMergeRefusesUncommittedAgentWork(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	res := agentCommit(t, f, "b.txt", "b\n")
	os.WriteFile(filepath.Join(res.Worktree, "late.txt"), []byte("x\n"), 0o644)
	_, e := f.m.Merge(context.Background(), res.RunID)
	wantCode(t, e, "MERGE_BLOCKED")
	if !strings.Contains(e.Msg, "1 uncommitted") {
		t.Fatal(e.Msg)
	}
}

func TestMergeConflictInCheckoutLeavesNothing(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	res := agentCommit(t, f, "a.txt", "agent\n")
	os.WriteFile(filepath.Join(f.repo, "a.txt"), []byte("human\n"), 0o644)
	git(t, f.repo, "commit", "-am", "human edit")
	before := git(t, f.repo, "rev-parse", "HEAD")

	_, e := f.m.Merge(context.Background(), res.RunID)
	wantCode(t, e, "MERGE_CONFLICT")
	if !strings.Contains(e.Msg, "a.txt") {
		t.Fatal(e.Msg)
	}
	if git(t, f.repo, "rev-parse", "HEAD") != before || git(t, f.repo, "status", "--porcelain", "--untracked-files=no") != "" {
		t.Fatal("conflict left changes behind")
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".git", "MERGE_HEAD")); err == nil {
		t.Fatal("merge still in progress")
	}
}

func TestMergeWithoutCheckout(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	ctx := context.Background()
	res := agentCommit(t, f, "b.txt", "b\n")
	git(t, f.repo, "checkout", "-b", "elsewhere")
	os.WriteFile(filepath.Join(f.repo, "a.txt"), []byte("dirty but irrelevant\n"), 0o644)

	m, e := f.m.Merge(ctx, res.RunID)
	if e != nil {
		t.Fatal(e)
	}
	if m.Method != "ref" || m.Commit != git(t, f.repo, "rev-parse", "main") || parents(t, f.repo, "main") != 2 {
		t.Fatalf("%+v", m)
	}
	if git(t, f.repo, "show", "main:b.txt") != "b" {
		t.Fatal("b.txt not on main")
	}
	if b, _ := os.ReadFile(filepath.Join(f.repo, "a.txt")); string(b) != "dirty but irrelevant\n" {
		t.Fatal("checkout was touched")
	}
}

func TestMergeConflictWithoutCheckout(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	res := agentCommit(t, f, "a.txt", "agent\n")
	os.WriteFile(filepath.Join(f.repo, "a.txt"), []byte("human\n"), 0o644)
	git(t, f.repo, "commit", "-am", "human edit")
	git(t, f.repo, "checkout", "-b", "elsewhere")
	before := git(t, f.repo, "rev-parse", "main")

	_, e := f.m.Merge(context.Background(), res.RunID)
	wantCode(t, e, "MERGE_CONFLICT")
	if !strings.Contains(e.Msg, "a.txt") || git(t, f.repo, "rev-parse", "main") != before {
		t.Fatal(e.Msg)
	}
}

// TestMergeIgnoresBatonAgentsMD: the untracked AGENTS.md Baton writes for codex (a copy of
// the brief) is not agent work; once the agent edits it, it is.
func TestMergeIgnoresBatonAgentsMD(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	res := agentCommit(t, f, "b.txt", "b\n")
	brief, err := os.ReadFile(filepath.Join(res.Worktree, "BATON_BRIEF.md"))
	if err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(res.Worktree, "AGENTS.md")
	os.WriteFile(agents, append(brief, "edited\n"...), 0o644)
	_, e := f.m.Merge(context.Background(), res.RunID)
	wantCode(t, e, "MERGE_BLOCKED")
	os.WriteFile(agents, brief, 0o644)
	if stat, err := f.m.DiffStat(context.Background(), res.RunID); err != nil || !strings.HasSuffix(stat, "; 0 uncommitted") {
		t.Fatalf("diff stat: %q %v", stat, err)
	}
	if _, e := f.m.Merge(context.Background(), res.RunID); e != nil {
		t.Fatalf("merge with baton's AGENTS.md: %v", e)
	}
}
