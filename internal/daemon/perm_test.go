package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/whosabhiraj/baton/internal/paths"
)

func TestEnsureDataDirPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	home := t.TempDir()
	dd := filepath.Join(home, "projects", "x-1234")
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsureDataDir(dd); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dd, filepath.Dir(dd), home} {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o700 {
			t.Fatalf("%s is %v", p, st.Mode().Perm())
		}
	}
}
