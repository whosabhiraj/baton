// Package launch is the real runs.Launcher: it starts agents under a detached supervisor
// (internal/supervise), tails their stdout through an adapter parser into the run store, and
// reports exit (with a diff stat) when the supervisor writes exit.json. Agents outlive the
// hub; Adopt picks them up again after a restart.
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/adapters"
	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/proc"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
	"github.com/whosabhiraj/baton/internal/spool"
	"github.com/whosabhiraj/baton/internal/store"
	"github.com/whosabhiraj/baton/internal/supervise"
)

// Files in the run directory.
const (
	stdoutFile   = "stdout.jsonl"
	offsetFile   = "stdout.offset"
	briefFile    = "brief.md"
	supervisorFn = "supervisor.json"
	reportedFile = "exit.reported"
)

const (
	killWait   = 15 * time.Second
	killGraceS = 5
	lostDetail = "lost after hub restart"
)

// Options configures a Launcher. Store and Runs may be set later with Bind (the hub that owns
// them is built after the launcher it is given).
type Options struct {
	Store  *store.Store
	Runs   *runs.Manager
	Config func() config.Config
	Exe    string // the baton binary (os.Executable); runs `__supervise` and is put on the agent's PATH
	Logger *slog.Logger
	Poll   time.Duration // tail/exit poll interval; default 250ms
}

// Launcher implements runs.Launcher.
type Launcher struct {
	o      Options
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	started time.Time // runs created after this are this process's own, never orphans

	mu      sync.Mutex
	watched map[string]*watcher
}

// New builds a Launcher.
func New(o Options) *Launcher {
	if o.Poll <= 0 {
		o.Poll = 250 * time.Millisecond
	}
	lg := o.Logger
	if lg == nil {
		lg = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Launcher{o: o, log: lg, ctx: ctx, cancel: cancel, watched: map[string]*watcher{}, started: time.Now()}
}

// Bind supplies the store, runs manager and live config once the hub exists.
func (l *Launcher) Bind(st *store.Store, rm *runs.Manager, cfg func() config.Config) {
	l.mu.Lock()
	l.o.Store, l.o.Runs, l.o.Config = st, rm, cfg
	l.mu.Unlock()
}

// Close stops all watchers (agents keep running).
func (l *Launcher) Close() {
	l.cancel()
	l.wg.Wait()
}

func (l *Launcher) store() *store.Store {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.o.Store
}

// Start launches the agent of a fresh run.
func (l *Launcher) Start(res runs.SpawnResult) error {
	st := l.store()
	if st == nil {
		return errors.New("launcher is not bound to a hub yet")
	}
	run := st.Get(res.RunID)
	if run == nil {
		return fmt.Errorf("unknown run %s", res.RunID)
	}
	cfg := l.config()
	acct, ok := cfg.Account(res.AccountID)
	if !ok {
		return fmt.Errorf("unknown account %q", res.AccountID)
	}
	if err := preflight(acct, res); err != nil {
		return err
	}
	ad, err := adapters.For(res.Kind, cfg)
	if err != nil {
		return err
	}
	argv, err := ad.Argv(*acct, res.Model, run.Record().Effort)
	if err != nil {
		return err
	}
	acctEnv := map[string]string{}
	if ep, ok := ad.(adapters.EnvProvider); ok {
		for k, v := range ep.Env() {
			acctEnv[k] = v
		}
	}
	for k, v := range acct.Env {
		acctEnv[k] = v
	}
	batonEnv := map[string]string{"BATON_BIN": l.o.Exe}
	for k, v := range res.Env {
		batonEnv[k] = v
	}
	env := proc.PrependPath(proc.BuildEnv(nil, acctEnv, batonEnv), filepath.Dir(l.o.Exe))

	runDir, err := filepath.Abs(res.RunDir)
	if err != nil {
		return err
	}
	brief := filepath.Join(runDir, briefFile)
	if err := os.WriteFile(brief, []byte(res.Brief), 0o644); err != nil {
		return fmt.Errorf("write brief: %w", err)
	}
	if _, err := supervise.Launch(l.o.Exe, supervise.Spec{Argv: argv, Env: env, Dir: res.Worktree,
		StdinFile: brief, RunDir: runDir, KillGraceS: killGraceS}); err != nil {
		return err
	}
	l.watch(run, ad)
	return nil
}

// Kill stops the run's agent tree, then records `killed`; the watcher records `exit`. Safe to
// call repeatedly.
func (l *Launcher) Kill(runID string) error {
	st := l.store()
	if st == nil {
		return errors.New("launcher is not bound to a hub yet")
	}
	run := st.Get(runID)
	if run == nil {
		return fmt.Errorf("unknown run %s", runID)
	}
	dir := run.Dir()
	if _, err := os.Stat(filepath.Join(dir, supervisorFn)); err != nil {
		return errors.New("no such agent")
	}
	l.mu.Lock()
	w := l.watched[runID]
	l.mu.Unlock()
	if w != nil {
		w.mu.Lock() // the watcher must not record exit before killed
		defer w.mu.Unlock()
	}
	if err := supervise.RequestKill(dir, killWait); err != nil {
		return err
	}
	if _, _, perr := run.AppendAdapter("killed", map[string]any{"reason": "killed on request"}, "adapter-killed"); perr != nil {
		return perr
	}
	return nil
}

// Adopt resumes every unfinished run that has a supervisor (call once, after the hub is up).
func (l *Launcher) Adopt() {
	st := l.store()
	if st == nil {
		return
	}
	for _, run := range st.List() {
		dir := run.Dir()
		if _, err := os.Stat(filepath.Join(dir, supervisorFn)); err != nil {
			l.endNeverStarted(run)
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, reportedFile)); err == nil {
			continue // exit already recorded
		}
		ad, err := adapters.For(run.Record().Kind, l.config())
		if err != nil {
			l.log.Warn("adopt: no adapter, output will not be parsed", "run", run.ID(), "err", err)
			ad = nil
		}
		l.log.Info("adopting run", "run", run.ID(), "alive", supervise.Alive(dir))
		l.watch(run, ad)
	}
}

// endNeverStarted records `exit` for a live run this launcher would have started but whose
// supervisor never wrote supervisor.json (the hub stopped during spawn). Dev/mock runs have no
// worktree branch or run-dir brief and are left alone, as are runs created since this process
// started (a spawn in progress).
func (l *Launcher) endNeverStarted(run *store.Run) {
	rec := run.Record()
	if run.Ended() || rec.Branch == "" || rec.Worktree == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(run.Dir(), "BATON_BRIEF.md")); err != nil {
		return
	}
	if t, err := time.Parse(proto.TSFormat, rec.CreatedAt); err != nil || t.After(l.started) {
		return
	}
	p := map[string]any{"code": -1, "signal": nil, "diff_stat": nil, "duration_s": 0.0,
		"detail": "never started (hub stopped during spawn)"}
	if _, _, perr := run.AppendAdapter("exit", p, "adapter-exit"); perr != nil {
		l.log.Error("cannot record exit of never-started run", "run", run.ID(), "err", perr.Error())
		return
	}
	l.log.Warn("run never started; marked exited", "run", run.ID())
}

func (l *Launcher) watch(run *store.Run, ad adapters.Adapter) {
	w := &watcher{l: l, run: run, dir: run.Dir()}
	var parser adapters.Parser
	if ad != nil {
		parser = ad.Parser()
	}
	w.parser = parser
	l.mu.Lock()
	if _, dup := l.watched[run.ID()]; dup {
		l.mu.Unlock()
		return
	}
	l.watched[run.ID()] = w
	l.mu.Unlock()
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		w.loop()
	}()
}

// watcher tails one run's stdout and reports its exit.
type watcher struct {
	l      *Launcher
	run    *store.Run
	dir    string
	parser adapters.Parser
	tail   *spool.Tailer
	mu     sync.Mutex // held while recording exit/killed
}

func (w *watcher) init() {
	t := &spool.Tailer{Path: filepath.Join(w.dir, stdoutFile), OffsetPath: filepath.Join(w.dir, offsetFile)}
	t.Warn = func(code, msg string, off int64) {
		w.l.log.Warn("stdout", "run", w.run.ID(), "code", code, "msg", msg, "offset", off)
	}
	t.Handle = w.handle
	w.tail = t
	w.warm()
}

// warm feeds the already-consumed part of stdout to the parser (events discarded) so stateful
// parsers (cumulative usage) are right after a restart.
func (w *watcher) warm() {
	if w.parser == nil {
		return
	}
	off, err := os.ReadFile(filepath.Join(w.dir, offsetFile))
	if err != nil {
		return
	}
	var n int64
	fmt.Sscan(string(off), &n)
	if n <= 0 {
		return
	}
	scratch := &spool.Tailer{Path: filepath.Join(w.dir, stdoutFile)}
	scratch.Handle = func(line []byte, o int64) error {
		if o < n {
			w.parser.Parse(line)
		}
		return nil
	}
	_ = scratch.Poll() // no OffsetPath: nothing persisted
}

func (w *watcher) handle(line []byte, off int64) error {
	if w.parser == nil {
		return nil
	}
	for i, ev := range w.parser.Parse(line) {
		id := fmt.Sprintf("adapter@%d", off)
		if i > 0 {
			id += fmt.Sprintf("#%d", i)
		}
		if _, _, perr := w.run.AppendAdapter(ev.Type, ev.Payload, id); perr != nil {
			if perr.Code == proto.CodeInternal {
				return perr // storage trouble: retry the line next poll
			}
			w.l.log.Warn("adapter event rejected", "run", w.run.ID(), "type", ev.Type, "err", perr.Error())
		}
	}
	return nil
}

func (w *watcher) loop() {
	w.init()
	tick := time.NewTicker(w.l.o.Poll)
	defer tick.Stop()
	gone := 0
	for {
		_ = w.tail.Poll()
		ex, done := supervise.ReadExit(w.dir)
		switch {
		case done:
			_ = w.tail.Poll() // output is final now
			w.finish(ex)
			return
		case !supervise.Alive(w.dir):
			// Agent gone without exit.json: give the supervisor a moment to write it.
			if gone++; gone >= 8 {
				_ = w.tail.Poll()
				w.finish(nil)
				return
			}
		default:
			gone = 0
		}
		select {
		case <-w.l.ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// finish records `exit` once (ex == nil: the supervisor vanished without a verdict).
func (w *watcher) finish(ex *supervise.Exit) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := map[string]any{"code": -1, "signal": nil, "diff_stat": nil, "duration_s": 0.0}
	if ex == nil {
		p["detail"] = lostDetail
	} else {
		p["code"], p["signal"], p["duration_s"] = ex.Code, ex.Signal, ex.DurationS
		if ex.Killed {
			p["killed"] = true
		}
	}
	if rm := w.l.o.Runs; rm != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if ds, err := rm.DiffStat(ctx, w.run.ID()); err == nil {
			p["diff_stat"] = ds
		}
		cancel()
	}
	if _, _, perr := w.run.AppendAdapter("exit", p, "adapter-exit"); perr != nil {
		w.l.log.Error("cannot record exit", "run", w.run.ID(), "err", perr.Error())
		return
	}
	_ = os.WriteFile(filepath.Join(w.dir, reportedFile), nil, 0o644)
	w.l.mu.Lock()
	delete(w.l.watched, w.run.ID())
	w.l.mu.Unlock()
}

func (l *Launcher) config() config.Config {
	l.mu.Lock()
	f := l.o.Config
	l.mu.Unlock()
	if f == nil {
		return config.Config{}
	}
	return f()
}
