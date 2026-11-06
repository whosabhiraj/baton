// Package mock runs `baton mock`: a foreground hub on a scratch repo and data dir, driven by a
// fake conductor and fake agents that use the real HTTP API. Nothing here starts a real agent.
package mock

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/whosabhiraj/baton/internal/conductor"
	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/logx"
	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
)

// Options configures a mock session.
type Options struct {
	Speed float64 // scenario speed, 1 = about three minutes for the opening script
	Seed  int64
	Once  bool      // stop after the opening script (the hub keeps serving)
	Port  int       // preferred port (7777 for the CLI); 0 = any free port
	Out   io.Writer // progress lines; nil = silent
}

// Dir is the mock data folder (<BATON_HOME>/mock).
func Dir() string { return filepath.Join(paths.BatonHome(), "mock") }

// Reset deletes the mock data folder, and only that folder.
func Reset() error { return os.RemoveAll(Dir()) }

const configYAML = `# Written by baton mock; edit freely, it is rewritten on every start.
hub:
  heartbeat_timeout_s: 20
  idle_stop_min: 0
  open_browser: true
  mode: propose
  approvals:
    merge: true
conductor:
  account: claude-a
  model: opus
  auto_wake: false   # the mock conductor is a stand-in (no model): don't nudge it
accounts:
  - id: claude-a
    label: "Claude A (conductor)"
    kind: claude
    color: "#ff8a3d"
    models: [opus, sonnet]
    roles: [conductor, planner, reviewer]
    max_concurrent: 2
  - id: claude-b
    label: "Claude B"
    kind: claude
    color: "#ffd166"
    models: [sonnet, opus]
    roles: [implementer, tester]
    max_concurrent: 3
  - id: codex-1
    label: "Codex"
    kind: codex
    color: "#4cc9f0"
    models: [default]
    roles: [implementer, refactorer]
    max_concurrent: 3
`

// ConfigPath is the mock config file (the user's own config is never read or written).
func ConfigPath() string { return filepath.Join(Dir(), "config.yaml") }

// prepare creates the scratch repo and the config file.
func prepare() (repo string, err error) {
	repo = filepath.Join(Dir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return "", err
	}
	if !gitx.IsRepo(repo) {
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Mock project\n\nScratch repo for baton mock.\n"), 0o644); err != nil {
			return "", err
		}
		if err := gitx.Init(repo); err != nil {
			return "", err
		}
	}
	return repo, os.WriteFile(ConfigPath(), []byte(configYAML), 0o644)
}

// Run starts the mock hub and engine and blocks until ctx is cancelled (or the hub stops).
// ready, if set, is called once the hub is listening.
func Run(ctx context.Context, o Options, ready func(h *hub.Hub, e *Engine)) error {
	if o.Speed <= 0 {
		o.Speed = 1
	}
	repo, err := prepare()
	if err != nil {
		return err
	}
	stall := max(time.Duration(20/o.Speed*float64(time.Second)), 400*time.Millisecond)
	e := newEngine(o, stall)
	e.ctx = ctx
	dataDir := filepath.Join(Dir(), "data")
	lg := (*slog.Logger)(nil)
	if l, closer, err := logx.Open(dataDir, slog.LevelInfo); err == nil {
		lg = l
		defer closer.Close()
	}
	exe, _ := os.Executable()
	h, err := hub.Start(ctx, hub.Options{DataDir: dataDir, Repo: repo, GlobalConfig: ConfigPath(), Dev: true, FakeMerges: true,
		Port: o.Port, PortFallback: true, IdleStop: -1, HeartbeatTimeout: stall, Logger: lg,
		Launcher: e, OnApproval: e.OnApproval,
		// the chat works too: the baton binary itself plays the conductor CLI (conductor.FakeCLI)
		ConductorExe: exe, ConductorCLI: exe, ConductorEnv: map[string]string{conductor.FakeEnv: "1"}})
	if err != nil {
		return err
	}
	e.hub = h
	// runs left over from an earlier mock session have no agent any more
	for _, r := range h.Store().List() {
		if !r.Ended() {
			_, _ = r.AppendHub("exit", map[string]any{"code": -1, "signal": nil, "diff_stat": nil, "duration_s": 0, "detail": "mock restarted"})
		}
	}
	h.SetConductor(proto.ConductorState{State: "idle", Account: "claude-a", Model: "opus"})
	if ready != nil {
		ready(h, e)
	}
	e.conduct(ctx)
	e.stopAgents()
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if errs := e.Errors(); len(errs) > 0 && o.Out != nil {
		fmt.Fprintf(o.Out, "mock: %d protocol errors, first: %s\n", len(errs), errs[0])
	}
	return h.Close(sctx)
}
