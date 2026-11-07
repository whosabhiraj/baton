package runs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/runs"
)

func TestMergeBoundToReviewedHead(t *testing.T) {
	gitIdentity(t)
	f := newFixture(t, baseCfg())
	ctx := context.Background()
	res := agentCommit(t, f, "b.txt", "b\n")
	endRun(t, f, res.RunID)
	head, e := f.m.BranchHead(ctx, res.RunID)
	if e != nil || head == "" {
		t.Fatal(e)
	}
	// the agent commits again after review
	os.WriteFile(filepath.Join(res.Worktree, "late.txt"), []byte("x\n"), 0o644)
	git(t, res.Worktree, "add", "late.txt")
	git(t, res.Worktree, "commit", "-m", "late")
	_, e = f.m.Merge(runs.WithHead(ctx, head), res.RunID)
	wantCode(t, e, "MERGE_BLOCKED")
	if !strings.Contains(e.Msg, "moved since review") {
		t.Fatal(e.Msg)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "b.txt")); err == nil {
		t.Fatal("merged despite moved branch")
	}
	// unchanged tip merges
	head, _ = f.m.BranchHead(ctx, res.RunID)
	if _, e = f.m.Merge(runs.WithHead(ctx, head), res.RunID); e != nil {
		t.Fatal(e)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "late.txt")); err != nil {
		t.Fatal("reviewed commits not merged")
	}
}

func TestSpawnKindMustMatchAccount(t *testing.T) {
	f := newFixture(t, baseCfg())
	r := req("claude-b")
	r.Kind = "opencode"
	_, e := f.m.Spawn(context.Background(), r)
	wantCode(t, e, "INVALID_PAYLOAD")
	r.Kind = "claude"
	if _, e := f.m.Spawn(context.Background(), r); e != nil {
		t.Fatal(e)
	}
}
