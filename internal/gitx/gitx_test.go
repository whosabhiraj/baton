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

func TestUnsafeRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if !IsUnsafeRoot(home) {
		t.Fatal("home folder not detected as unsafe")
	}
	root := filepath.VolumeName(home) + string(filepath.Separator)
	if !IsUnsafeRoot(root) {
		t.Fatalf("filesystem root %q not detected as unsafe", root)
	}
	sub := filepath.Join(home, "proj")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsUnsafeRoot(sub) {
		t.Fatal("project folder under home flagged as unsafe")
	}
	if err := Init(home); err != ErrUnsafeRoot {
		t.Fatalf("Init(home) = %v, want ErrUnsafeRoot", err)
	}
	// a stray repo AT home must not make subfolders a project
	if _, err := Run(t.Context(), home, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectTopLevel(sub); err != ErrUnsafeRoot {
		t.Fatalf("ProjectTopLevel(sub of home repo) = %v, want ErrUnsafeRoot", err)
	}
	// a real project under home is fine
	if err := Init(sub); err != nil {
		t.Fatal(err)
	}
	if top, err := ProjectTopLevel(sub); err != nil || !sameFile(t, top, sub) {
		t.Fatalf("ProjectTopLevel(project) = %q, %v", top, err)
	}
}
