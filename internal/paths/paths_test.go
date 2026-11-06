package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Golden values computed with the TypeScript reference (hub/paths.ts projectDataDir) on
// Windows, using paths that do not exist (so no realpath is involved).
func TestProjectDataDirGoldenWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("golden paths are Windows paths")
	}
	cases := map[string]string{
		`C:\Nonexistent\My Repo`:                    `My_Repo-4af17107`,
		`C:\Nonexistent\Proj_X.y-1`:                 `Proj_X.y-1-29ce17b3`,
		`D:\zz_nope\ÄÖ repo`:                        `___repo-361683b0`,
		`C:\nonexistent\` + strings.Repeat("a", 60): strings.Repeat("a", 40) + `-2e5a1c1d`,
		`C:\Nonexistent\😀x`:                         `__x-deffe416`,
		`C:\`:                                       `root-3dc8472e`,
	}
	for in, want := range cases {
		got := ProjectDataDir(in, "H")
		if exp := filepath.Join("H", "projects", want); got != exp {
			t.Errorf("ProjectDataDir(%q) = %q, want %q", in, got, exp)
		}
	}
}

func TestWindowsLowercaseHash(t *testing.T) {
	a := dirName(`C:\Users\Foo\Proj`, "windows")
	b := dirName(`c:\users\foo\proj`, "windows")
	if a[strings.LastIndex(a, "-"):] != b[strings.LastIndex(b, "-"):] {
		t.Errorf("windows hash should be case-insensitive: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "Proj-") {
		t.Errorf("name keeps case: %s", a)
	}
}

func TestUnixAndMacHash(t *testing.T) {
	// h8 values computed with node: sha256 of the exact / lower-cased string.
	if got := dirName("/nonexistent/My Repo", "linux"); got != "My_Repo-46d4a69d" {
		t.Errorf("linux: %s", got)
	}
	if got := dirName("/Users/Foo/Proj", "linux"); got != "Proj-2faff442" {
		t.Errorf("linux case-sensitive: %s", got)
	}
	if got := dirName("/Users/Foo/Proj", "darwin"); got != "Proj-37493ec4" {
		t.Errorf("darwin lower-cases the key: %s", got)
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitizeName("a b/c:d*é"); got != "a_b_c_d__" {
		t.Errorf("got %q", got)
	}
	if got := sanitizeName(strings.Repeat("x", 100)); len(got) != 40 {
		t.Errorf("len %d", len(got))
	}
	if sanitizeName("") != "root" || sanitizeName("/") != "root" {
		t.Error("root fallback")
	}
}

func TestSymlinkResolvedAndExisting(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	got := ProjectDataDir(dir, home)
	real, _ := filepath.EvalSymlinks(dir)
	want := filepath.Join(home, "projects", dirName(real, runtime.GOOS))
	if got != want {
		t.Errorf("got %s want %s", got, want)
	}
	// Relative path is made absolute.
	wd, _ := os.Getwd()
	if ProjectDataDir(".", home) != ProjectDataDir(wd, home) {
		t.Error("relative path should equal absolute")
	}
}

func TestRunDirs(t *testing.T) {
	d := filepath.Join("x", "y")
	if RunsDir(d) != filepath.Join(d, "runs") || RunDir(d, "r_1") != filepath.Join(d, "runs", "r_1") {
		t.Error("run dirs")
	}
}

func TestBatonHome(t *testing.T) {
	t.Setenv("BATON_HOME", "")
	h := BatonHome()
	if filepath.Base(h) != ".baton" {
		t.Errorf("default: %s", h)
	}
	t.Setenv("BATON_HOME", "rel")
	if !filepath.IsAbs(BatonHome()) {
		t.Error("BATON_HOME should be made absolute")
	}
}

func TestInboxNames(t *testing.T) {
	if InboxFileName(12) != "000012.json" || InboxFileName(1234567) != "1234567.json" {
		t.Error("InboxFileName")
	}
	for name, n := range map[string]int{"000001.json": 1, "000012.json": 12, "1234567.json": 1234567} {
		if got, ok := ParseInboxFileName(name); !ok || got != n {
			t.Errorf("%s -> %d %v", name, got, ok)
		}
	}
	for _, bad := range []string{"000000.json", "1.json", ".tmp-123", "000001.json.tmp-9", "abc.json", ".read", "00001a.json", ""} {
		if _, ok := ParseInboxFileName(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}
