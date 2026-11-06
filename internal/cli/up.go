package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/paths"
)

// Seams for tests.
var (
	startHub  = hub.Start
	closeHub  = func(h *hub.Hub, ctx context.Context) error { return h.Close(ctx) }
	upStopCtx = func() (context.Context, context.CancelFunc) {
		return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	}
)

// repoRoot returns the git top-level of dir, or dir itself when not in a repo.
func repoRoot(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return filepath.FromSlash(s)
		}
	}
	return dir
}

func (c *ctx) cmdUp(o opts) (int, error) {
	opt := hub.Options{Dev: o.flag("dev"), Port: 7777, PortFallback: true}
	if o.has("port") {
		n, err := strconv.Atoi(strings.TrimSpace(o.get("port")))
		if err != nil || n < 0 || n > 65535 {
			return 0, usagef("--port must be 0-65535, got %q", o.get("port"))
		}
		opt.Port, opt.PortFallback = n, false
	}
	if o.has("data-dir") {
		opt.DataDir = o.get("data-dir")
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return 0, err
		}
		opt.DataDir = paths.ProjectDataDir(repoRoot(cwd), paths.BatonHome())
	}
	stop, cancel := upStopCtx()
	defer cancel()
	h, err := startHub(stop, opt)
	if err != nil {
		if errors.Is(err, hub.ErrAlreadyRunning) {
			return 0, fmt.Errorf("a hub is already running for this project (%s)", opt.DataDir)
		}
		return 0, err
	}
	fmt.Fprintf(c.stdout, "baton hub: %s\n", h.URL)
	fmt.Fprintf(c.stdout, "admin token: %s\n", h.AdminToken)
	fmt.Fprintf(c.stdout, "data dir: %s\n", h.DataDir)
	if f := o.get("admin-token-file"); o.has("admin-token-file") {
		if err := os.WriteFile(f, []byte(h.AdminToken), 0o600); err != nil {
			fmt.Fprintf(c.stderr, "baton: warning: could not write admin token file (%s)\n", err.Error())
		}
	}
	<-stop.Done()
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	if err := closeHub(h, sctx); err != nil {
		return 0, err
	}
	return 0, nil
}
