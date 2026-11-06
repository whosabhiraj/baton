package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/mock"
)

func stubMock(t *testing.T) *mock.Options {
	t.Helper()
	var got mock.Options
	old := runMock
	t.Cleanup(func() { runMock = old })
	runMock = func(_ context.Context, o mock.Options, _ func(*hub.Hub, *mock.Engine)) error { got = o; return nil }
	return &got
}

func TestMockFlags(t *testing.T) {
	t.Setenv("BATON_HOME", t.TempDir())
	got := stubMock(t)
	if code, _, e := runMain("", "mock"); code != 0 || got.Speed != 1 || got.Seed != 1 || got.Once || got.Port != 7777 {
		t.Fatal(code, e, *got)
	}
	if code, _, e := runMain("", "mock", "--speed", "2.5", "--seed=7", "--once", "--no-browser"); code != 0 || got.Speed != 2.5 || got.Seed != 7 || !got.Once {
		t.Fatal(code, e, *got)
	}
	for _, bad := range [][]string{{"--speed", "0"}, {"--speed", "fast"}, {"--seed", "x"}} {
		if code, _, e := runMain("", append([]string{"mock"}, bad...)...); code != 2 || !strings.Contains(e, "--") {
			t.Fatal(bad, code, e)
		}
	}
}

func TestMockResetOnlyDeletesMockDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BATON_HOME", home)
	stubMock(t)
	for _, f := range []string{"mock/repo/README.md", "config.yaml", "projects/p/x"} {
		p := filepath.Join(home, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	if code, _, e := runMain("", "mock", "--reset"); code != 0 {
		t.Fatal(code, e)
	}
	if _, err := os.Stat(filepath.Join(home, "mock")); !os.IsNotExist(err) {
		t.Error("mock dir not deleted")
	}
	for _, f := range []string{"config.yaml", "projects/p/x"} {
		if _, err := os.Stat(filepath.Join(home, f)); err != nil {
			t.Errorf("%s was deleted", f)
		}
	}
}
