// Package e2e holds black-box tests: they build the real baton binary once and spawn it.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

var (
	// binPath is the freshly built baton binary; empty if the build failed.
	binPath string
	// buildErr explains why binPath is empty.
	buildErr string
	// repoRoot is the module root (parent of e2e/).
	repoRoot string
)

func TestMain(m *testing.M) { os.Exit(realMain(m)) }

func realMain(m *testing.M) int {
	wd, err := os.Getwd()
	if err != nil {
		buildErr = err.Error()
		return m.Run()
	}
	repoRoot = filepath.Dir(wd)
	tmp, err := os.MkdirTemp("", "baton-e2e-bin-")
	if err != nil {
		buildErr = err.Error()
		return m.Run()
	}
	defer os.RemoveAll(tmp)

	name := "baton"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(tmp, name)
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	// Retry: Defender/AV can briefly lock a fresh build output on Windows.
	var b []byte
	for attempt := 0; attempt < 3; attempt++ {
		cmd := exec.Command(goBin, "build", "-o", out, "./cmd/baton")
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err = cmd.CombinedOutput(); err == nil {
			binPath = out
			break
		}
		time.Sleep(time.Second)
	}
	if binPath == "" {
		buildErr = fmt.Sprintf("go build ./cmd/baton failed: %v\n%s", err, b)
		fmt.Fprintln(os.Stderr, "e2e: "+buildErr)
	}
	return m.Run()
}

// requireBin skips the test (or fails it when CI is set) if the binary could not be built.
func requireBin(t *testing.T) {
	t.Helper()
	if binPath != "" {
		return
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("baton binary not built: %s", buildErr)
	}
	t.Skipf("baton binary not built, skipping e2e: %s", buildErr)
}
