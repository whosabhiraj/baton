package runs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/runs"
)

func gcFixture(t *testing.T) (*fixture, *runs.SpawnResult, *runs.SpawnResult) {
	cfg := baseCfg()
	cfg.Accounts[0].MaxConcurrent = 5
	f := newFixture(t, cfg)
	a, e := f.m.Spawn(context.Background(), req("claude-b"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := f.m.Spawn(context.Background(), req("claude-b")) // stays live
	if e != nil {
		t.Fatal(e)
	}
	endRun(t, f, a.RunID)
	return f, a, b
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestGCPolicyAndLive(t *testing.T) {
	f, a, b := gcFixture(t)
	ctx := context.Background()
	future := time.Now().AddDate(0, 0, 100)
	if rep := f.m.GC(ctx, future, runs.GCOptions{Force: true}); len(rep.Items) != 0 {
		t.Fatalf("keep policy: %+v", rep)
	}
	f.cfg.Hub.Worktrees = config.Worktrees{Cleanup: config.CleanupRemoveAfter, Days: 14}
	if rep := f.m.GC(ctx, time.Now(), runs.GCOptions{}); len(rep.Items) != 0 {
		t.Fatalf("too early: %+v", rep)
	}
	// dry run: reported, nothing changed
	rep := f.m.GC(ctx, future, runs.GCOptions{DryRun: true})
	if len(rep.Items) != 1 || rep.Items[0].RunID != a.RunID || rep.Items[0].What != "worktree" || rep.Items[0].Skipped != "" {
		t.Fatalf("dry: %+v", rep)
	}
	if !exists(a.Worktree) {
		t.Fatal("dry run removed the worktree")
	}
	rep = f.m.GC(ctx, future, runs.GCOptions{Force: true})
	if len(rep.Items) != 1 || rep.Items[0].Skipped != "" {
		t.Fatalf("real: %+v", rep)
	}
	if exists(a.Worktree) {
		t.Fatal("worktree remains")
	}
	if !exists(b.Worktree) {
		t.Fatal("live worktree removed")
	}
	if git(t, f.repo, "branch", "--list", a.Branch) == "" {
		t.Fatal("branch deleted")
	}
	if f.st.Get(a.RunID) == nil {
		t.Fatal("run forgotten without runs_keep_days")
	}
}

func TestGCUncommittedSkipVsForce(t *testing.T) {
	f, a, _ := gcFixture(t)
	ctx := context.Background()
	f.cfg.Hub.Worktrees = config.Worktrees{Cleanup: config.CleanupRemoveAfter, Days: 1}
	os.WriteFile(filepath.Join(a.Worktree, "wip.txt"), []byte("x\n"), 0o644)
	future := time.Now().AddDate(0, 0, 5)
	rep := f.m.GC(ctx, future, runs.GCOptions{})
	if len(rep.Items) != 1 || rep.Items[0].Skipped != "uncommitted changes" || !exists(a.Worktree) {
		t.Fatalf("%+v", rep)
	}
	rep = f.m.GC(ctx, future, runs.GCOptions{Force: true})
	if len(rep.Items) != 1 || rep.Items[0].Skipped != "" || exists(a.Worktree) {
		t.Fatalf("%+v", rep)
	}
}

func TestGCRemoveOnMerge(t *testing.T) {
	f, a, _ := gcFixture(t)
	f.cfg.Hub.Worktrees = config.Worktrees{Cleanup: config.CleanupRemoveOnMerge}
	if rep := f.m.GC(context.Background(), time.Now(), runs.GCOptions{}); len(rep.Items) != 0 {
		t.Fatalf("unmerged: %+v", rep)
	}
	if err := f.st.Get(a.RunID).SetExtra("merged", true); err != nil {
		t.Fatal(err)
	}
	rep := f.m.GC(context.Background(), time.Now(), runs.GCOptions{})
	if len(rep.Items) != 1 || rep.Items[0].Reason != "merged" || exists(a.Worktree) {
		t.Fatalf("%+v", rep)
	}
}

func TestGCRunFolders(t *testing.T) {
	f, a, b := gcFixture(t)
	ctx := context.Background()
	f.cfg.Hub.RunsKeepDays = 30
	if rep := f.m.GC(ctx, time.Now().AddDate(0, 0, 29), runs.GCOptions{}); len(rep.Items) != 0 {
		t.Fatalf("too early: %+v", rep)
	}
	future := time.Now().AddDate(0, 0, 31)
	os.WriteFile(filepath.Join(a.Worktree, "wip.txt"), []byte("x\n"), 0o644)
	rep := f.m.GC(ctx, future, runs.GCOptions{})
	if len(rep.Items) != 2 || rep.Items[0].What != "worktree" || rep.Items[0].Skipped == "" || rep.Items[1].What != "run" || rep.Items[1].Skipped == "" {
		t.Fatalf("%+v", rep)
	}
	if f.st.Get(a.RunID) == nil || !exists(a.RunDir) {
		t.Fatal("run removed despite dirty worktree")
	}
	rep = f.m.GC(ctx, future, runs.GCOptions{DryRun: true, Force: true})
	if len(rep.Items) != 2 || !exists(a.RunDir) {
		t.Fatalf("%+v", rep)
	}
	rep = f.m.GC(ctx, future, runs.GCOptions{Force: true})
	if len(rep.Items) != 2 || rep.Items[1].Skipped != "" {
		t.Fatalf("%+v", rep)
	}
	if f.st.Get(a.RunID) != nil || exists(a.RunDir) || exists(a.Worktree) {
		t.Fatal("run not removed")
	}
	if git(t, f.repo, "branch", "--list", a.Branch) == "" {
		t.Fatal("branch deleted")
	}
	if f.st.Get(b.RunID) == nil || !exists(b.Worktree) {
		t.Fatal("live run touched")
	}
	// 0 = never
	f.cfg.Hub.RunsKeepDays = 0
	if rep := f.m.GC(ctx, future.AddDate(1, 0, 0), runs.GCOptions{Force: true}); len(rep.Items) != 0 {
		t.Fatalf("%+v", rep)
	}
}
