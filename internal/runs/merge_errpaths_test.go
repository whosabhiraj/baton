package runs_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/store"
)

func TestErrPathsMergeAndDiffUnknownOrBranchless(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	ctx := context.Background()
	if _, e := f.m.Merge(ctx, "r_nope"); e == nil || e.Code != proto.CodeNotFound || e.Status != 404 {
		t.Fatalf("merge unknown run: %v", e)
	}
	if _, e := f.m.Diff(ctx, "r_nope", 0); e == nil || e.Code != proto.CodeNotFound {
		t.Fatalf("diff unknown run: %v", e)
	}
	// a run that was never spawned into a worktree has no branch to merge
	if _, _, err := f.st.Create(store.CreateOpts{RunID: "r_plain", AgentID: "a-plain"}); err != nil {
		t.Fatal(err)
	}
	_, e := f.m.Merge(ctx, "r_plain")
	wantCode(t, e, proto.CodeMergeBlocked)
	if e.Status != 409 {
		t.Fatal(e.Status)
	}
}

func TestErrPathsMergeBlockedBranchOrBaseGone(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	ctx := context.Background()
	res := agentCommit(t, f, "b.txt", "agent\n")

	// base is no longer a local branch
	git(t, f.repo, "branch", "-m", "main", "moved")
	_, e := f.m.Merge(ctx, res.RunID)
	wantCode(t, e, proto.CodeMergeBlocked)
	git(t, f.repo, "branch", "-m", "moved", "main")

	// base checked out in a different worktree than the project checkout
	git(t, f.repo, "checkout", "-b", "side")
	other := filepath.Join(t.TempDir(), "other")
	git(t, f.repo, "worktree", "add", other, "main")
	_, e = f.m.Merge(ctx, res.RunID)
	wantCode(t, e, proto.CodeMergeBlocked)
	git(t, f.repo, "worktree", "remove", "--force", other)
	git(t, f.repo, "checkout", "main")

	// agent branch deleted (worktree removed first, git refuses otherwise)
	git(t, f.repo, "worktree", "remove", "--force", res.Worktree)
	git(t, f.repo, "branch", "-D", res.Branch)
	_, e = f.m.Merge(ctx, res.RunID)
	wantCode(t, e, proto.CodeMergeBlocked)
}

func TestErrPathsDiffGitError(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	res := agentCommit(t, f, "c.txt", "agent\n")
	git(t, f.repo, "worktree", "remove", "--force", res.Worktree)
	git(t, f.repo, "branch", "-D", res.Branch)
	_, e := f.m.Diff(context.Background(), res.RunID, 0)
	wantCode(t, e, proto.CodeGit)
	if e.Status != 500 {
		t.Fatal(e.Status)
	}
}
