package launch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/whosabhiraj/baton/internal/gitx"
)

// TestGitWritePaths: a commit on the agent's branch needs the worktree's git dir, objects and
// the branch's ref/log folders of the main repo, and nothing that would let it move main.
func TestGitWritePaths(t *testing.T) {
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.MkdirAll(repo, 0o755)
	if err := gitx.Init(repo); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := gitx.Run(ctx, repo, "commit", "--allow-empty", "-m", "init"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	if _, err := gitx.Run(ctx, repo, "worktree", "add", "-b", "baton/a1", wt); err != nil {
		t.Fatal(err)
	}
	got, err := gitWritePaths(wt, "baton/a1")
	if err != nil {
		t.Fatal(err)
	}
	real := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return filepath.Clean(p)
		}
		return r
	}
	common := real(filepath.Join(repo, ".git"))
	want := []string{real(filepath.Join(common, "worktrees", "wt")), filepath.Join(common, "objects"),
		filepath.Join(common, "refs", "heads", "baton"), filepath.Join(common, "logs", "refs", "heads", "baton")}
	for i := range got {
		got[i] = real(got[i])
	}
	for i := range want {
		want[i] = real(want[i])
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	if st, err := os.Stat(want[3]); err != nil || !st.IsDir() {
		t.Fatalf("log dir not created: %v", err)
	}
	if _, err := gitWritePaths(filepath.Join(root, "nope"), "baton/a1"); err == nil {
		t.Fatal("not a git dir must fail")
	}
}
