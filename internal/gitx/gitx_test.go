package gitx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitTopLevelBranch(t *testing.T) {
	dir := t.TempDir()
	if IsRepo(dir) {
		t.Skip("temp dir is inside a git repo")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if !IsRepo(dir) {
		t.Fatal("not a repo after Init")
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	top, err := TopLevel(sub)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	got, _ := filepath.EvalSymlinks(top)
	if !sameFile(t, got, want) {
		t.Fatalf("TopLevel = %q, want %q", top, dir)
	}
	if b, err := CurrentBranch(dir); err != nil || b == "" {
		t.Fatalf("CurrentBranch = %q, %v", b, err)
	}
	if _, err := Run(t.Context(), dir, "no-such-command"); err == nil {
		t.Fatal("expected error")
	}
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}
