package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/daemon"
	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/launch"
	"github.com/whosabhiraj/baton/internal/logx"
	"github.com/whosabhiraj/baton/internal/paths"
)

// Seams for tests.
var (
	startHub  = hub.Start
	closeHub  = func(h *hub.Hub, ctx context.Context) error { return h.Close(ctx) }
	upStopCtx = func() (context.Context, context.CancelFunc) {
		return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	}
	loadConfig = func(repo string) *config.Snapshot {
		return config.Load(config.GlobalPath(), config.ProjectPath(repo))
	}
)

// project resolves the repo (may be "" for --data-dir without a repo) and the data dir.
// --project P names the repo; --data-dir D overrides the data dir (tests).
func project(o opts) (repo, dataDir string, err error) {
	if o.has("project") {
		repo = o.get("project")
	} else if cwd, e := os.Getwd(); e == nil {
		if top, e := gitx.ProjectTopLevel(cwd); e == nil {
			repo = top
		} else if !o.has("data-dir") {
			repo = cwd
		}
	}
	if o.has("data-dir") {
		return repo, o.get("data-dir"), nil
	}
	if repo == "" {
		return "", "", errors.New("cannot determine the project folder")
	}
	return repo, paths.ProjectDataDir(repo, paths.BatonHome()), nil
}

// cmdHubServe runs the hub in the foreground (what the detached daemon executes).
func (c *ctx) cmdHubServe(o opts) (int, error) {
	repo, dataDir, err := project(o)
	if err != nil {
		return 0, err
	}
	cfg := loadConfig(repo).Config
	opt := hub.Options{Dev: o.flag("dev"), DataDir: dataDir, Repo: repo, Port: cfg.Hub.Port, PortFallback: true, Bind: cfg.Hub.Bind}
	if cfg.Hub.HeartbeatTimeoutS > 0 {
		opt.HeartbeatTimeout = time.Duration(cfg.Hub.HeartbeatTimeoutS) * time.Second
	}
	if o.has("port") {
		n, err := strconv.Atoi(strings.TrimSpace(o.get("port")))
		if err != nil || n < 0 || n > 65535 {
			return 0, usagef("--port must be 0-65535, got %q", o.get("port"))
		}
		opt.Port, opt.PortFallback = n, false
	}
	// --admin-token-file F: if F exists and is non-empty, use its token; otherwise the hub's
	// token (persistent per project) is written to F after start.
	tokenFromFile := false
	if o.has("admin-token-file") {
		if b, err := os.ReadFile(o.get("admin-token-file")); err == nil {
			if tok := strings.TrimSpace(string(b)); tok != "" {
				opt.AdminToken, tokenFromFile = tok, true
			}
		}
	}
	level := slog.LevelInfo
	if opt.Dev {
		level = slog.LevelDebug
	}
	if lg, closer, err := logx.Open(dataDir, level); err == nil {
		opt.Logger = lg
		defer closer.Close()
	} else {
		fmt.Fprintf(c.stderr, "baton: warning: cannot open log (%s)\n", err.Error())
	}

	exe, _ := os.Executable()
	lau := launch.New(launch.Options{Exe: exe, Logger: opt.Logger})
	opt.Launcher = lau
	stop, cancel := upStopCtx()
	defer cancel()
	h, err := startHub(stop, opt)
	if err != nil {
		if errors.Is(err, hub.ErrAlreadyRunning) {
			return 0, fmt.Errorf("a hub is already running for this project (%s)", opt.DataDir)
		}
		return 0, err
	}
	lau.Bind(h.Store(), h.Runs(), h.Config)
	lau.Adopt()
	info := daemon.Info{PID: os.Getpid(), Port: h.Port, URL: h.URL, Instance: healthInstance(h.URL), Version: Version,
		Exe: exe, StartedAt: time.Now().UTC().Format(time.RFC3339), DataDir: h.DataDir, Project: repo}
	if err := daemon.WriteInfo(h.DataDir, info); err != nil {
		fmt.Fprintf(c.stderr, "baton: warning: cannot write %s (%s)\n", daemon.InfoFile, err.Error())
	}
	fmt.Fprintf(c.stdout, "baton hub: %s\n", h.URL)
	fmt.Fprintf(c.stdout, "admin token: %s\n", h.AdminToken)
	fmt.Fprintf(c.stdout, "data dir: %s\n", h.DataDir)
	if f := o.get("admin-token-file"); o.has("admin-token-file") && !tokenFromFile {
		if err := os.WriteFile(f, []byte(h.AdminToken), 0o600); err != nil {
			fmt.Fprintf(c.stderr, "baton: warning: could not write admin token file (%s)\n", err.Error())
		}
	}
	select {
	case <-stop.Done():
	case <-h.Done(): // POST /api/hub/shutdown
	}
	lau.Close() // agents keep running; the next hub re-adopts them
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	err = closeHub(h, sctx)
	_ = daemon.RemoveInfo(h.DataDir)
	return 0, err
}

// healthInstance reads the instance id the hub reports on /api/health ("" on failure).
func healthInstance(url string) string {
	cl := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := cl.Get(strings.TrimRight(url, "/") + "/api/health")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var m struct {
		Instance string `json:"instance"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return m.Instance
}

func adminTokenPath(dataDir string) string { return filepath.Join(dataDir, hub.AdminTokenFile) }
