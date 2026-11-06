// Package hub wires the store, spool tailer, event bus and HTTP API into one running hub.
package hub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/whosabhiraj/baton/internal/api"
	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/audit"
	"github.com/whosabhiraj/baton/internal/bus"
	"github.com/whosabhiraj/baton/internal/conductor"
	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/redact"
	"github.com/whosabhiraj/baton/internal/runs"
	"github.com/whosabhiraj/baton/internal/spool"
	"github.com/whosabhiraj/baton/internal/store"
	"github.com/whosabhiraj/baton/web"
)

// Files kept in the data dir by the hub itself.
const (
	LockFile       = "hub.lock"
	AdminTokenFile = "admin.token"
	PortFile       = "hub.port"
)

// portFallbackSpan is how many ports above the configured one are tried.
const portFallbackSpan = 20

// Options configures a hub instance.
type Options struct {
	DataDir          string        // project data dir (paths.ProjectDataDir)
	Bind             string        // default "127.0.0.1"; non-loopback refused in Phase 1
	Port             int           // 0 = OS-assigned
	PortFallback     bool          // if Port is taken, try the next free ports and remember the choice per project
	Dev              bool          // enables POST /api/dev/runs
	AdminToken       string        // "" = load/create <DataDir>/admin.token (persistent per project)
	SpoolPoll        time.Duration // default 250ms
	HeartbeatTimeout time.Duration // 0 = live config hub.heartbeat_timeout_s (account override per run); >0 fixes it (tests)
	Logger           *slog.Logger  // nil = discard

	Repo          string                   // git top-level of the target project; "" = no runs manager (POST /api/spawn -> 409 NOT_A_REPO)
	GlobalConfig  string                   // "" = config.GlobalPath()
	ProjectConfig string                   // "" = config.ProjectPath(Repo) when Repo is set, else none
	ConfigPoll    time.Duration            // config file poll interval; default 2s
	Launcher      runs.Launcher            // starts/kills agent processes (nil = spawn only records the run)
	OnApproval    func(approvals.Approval) // called after a human decision
	FakeMerges    bool                     // approved merges only mark the run merged (baton mock)
	// ConductorExe is the baton binary the conductor's MCP config runs (`baton mcp`); "" = this
	// hub has no conductor (the /api/conductor routes answer NO_CONDUCTOR). Needs Repo.
	ConductorExe  string
	ConductorCLI  string            // claude command for the conductor; default "claude" (tests: a fake)
	ConductorEnv  map[string]string // extra conductor env (tests)
	ConductorWake time.Duration     // auto-wake debounce; default 2s (tests)
	IdleStop      time.Duration     // 0 = config hub.idle_stop_min (0 there = never); <0 = disabled; >0 overrides the config
}

// Hub is a running hub.
type Hub struct {
	URL        string
	AdminToken string
	DataDir    string
	Port       int

	log       *slog.Logger
	lock      *fileLock
	store     *store.Store
	broker    *bus.Broker
	spool     *spool.Manager
	srv       *http.Server
	timeout   time.Duration // fixed heartbeat timeout (tests); 0 = from live config
	idleStop  time.Duration
	cfg       *config.Watcher
	audit     *audit.Log
	runs      *runs.Manager
	approvals *approvals.Store
	condMu    sync.Mutex
	conductor proto.ConductorState
	condTok   atomic.Pointer[[32]byte] // sha256 of the current conductor session token (A16)
	cond      *conductor.Manager       // nil = no conductor in this hub
	lastAdm   atomic.Int64             // unix nanos of the last admin API request
	stopCh    chan struct{}            // closed first on shutdown: ends SSE streams and background loops
	detWG     sync.WaitGroup
	serveWG   sync.WaitGroup
	done      chan struct{}
	closeMu   sync.Once
	closeErr  error
}

// ErrAlreadyRunning is returned by Start when another hub holds the data dir's lock.
var ErrAlreadyRunning = errors.New("hub already running for this project")

func isLoopback(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(bind, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Start takes the lock, opens the store, ingests outboxes, then listens.
// ctx is the hub lifetime, not a startup timeout: when it is cancelled the hub shuts down
// (same as Close). Pass context.Background() and call Close to control shutdown explicitly.
func Start(ctx context.Context, o Options) (*Hub, error) {
	if o.DataDir == "" {
		return nil, errors.New("hub: DataDir is required")
	}
	if o.Bind == "" {
		o.Bind = "127.0.0.1"
	}
	if !isLoopback(o.Bind) {
		return nil, fmt.Errorf("hub: refusing to bind %q: only loopback addresses are allowed", o.Bind)
	}
	if o.Port < 0 || o.Port > 65535 {
		return nil, fmt.Errorf("hub: invalid port %d", o.Port)
	}
	if o.SpoolPoll <= 0 {
		o.SpoolPoll = 250 * time.Millisecond
	}
	if o.HeartbeatTimeout < 0 {
		o.HeartbeatTimeout = 0
	}
	inner := o.Logger
	if inner == nil {
		inner = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	log := slog.New(redact.NewHandler(inner.Handler()))

	dataDir, err := filepath.Abs(o.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	lock, ok, err := tryLock(filepath.Join(dataDir, LockFile))
	if err != nil {
		return nil, fmt.Errorf("hub: lock %s: %w", LockFile, err)
	}
	if !ok {
		return nil, fmt.Errorf("%w (%s)", ErrAlreadyRunning, dataDir)
	}
	h := &Hub{DataDir: dataDir, log: log, lock: lock, timeout: o.HeartbeatTimeout,
		idleStop: o.IdleStop, stopCh: make(chan struct{}), done: make(chan struct{})}
	h.lastAdm.Store(time.Now().UnixNano())
	fail := func(err error) (*Hub, error) {
		h.cfg.Stop()
		if h.audit != nil {
			_ = h.audit.Close()
		}
		if h.store != nil {
			h.store.Close()
		}
		lock.release()
		return nil, err
	}

	h.AdminToken = o.AdminToken
	if h.AdminToken == "" {
		if h.AdminToken, err = loadOrCreateAdminToken(filepath.Join(dataDir, AdminTokenFile)); err != nil {
			return fail(fmt.Errorf("hub: admin token: %w", err))
		}
	}

	h.broker = bus.New(bus.DefaultRing, bus.DefaultSubBuffer)
	gp, pp := o.GlobalConfig, o.ProjectConfig
	if gp == "" {
		gp = config.GlobalPath()
	}
	if pp == "" && o.Repo != "" {
		pp = config.ProjectPath(o.Repo)
	}
	h.cfg = config.Watch(context.Background(), gp, pp, o.ConfigPoll, func(s *config.Snapshot) {
		issues := s.Issues
		if issues == nil {
			issues = []config.Issue{}
		}
		log.Info("config reloaded", "version", s.Version, "ok", s.OK(), "issues", len(s.Issues))
		h.broker.PublishNotice("config_reloaded", map[string]any{"version": s.Version, "ok": s.OK(), "issues": issues})
	})
	if s := h.cfg.Current(); !s.OK() {
		log.Warn("config has errors; using last good/default values", "issues", len(s.Issues))
	}
	if h.audit, err = audit.Open(dataDir); err != nil {
		return fail(fmt.Errorf("hub: open audit log: %w", err))
	}
	h.store, err = store.Open(dataDir, store.Options{Logger: log, Publish: func(ev proto.StoredEvent) { h.broker.Publish(ev) }})
	if err != nil {
		return fail(fmt.Errorf("hub: open store: %w", err))
	}
	if h.approvals, err = approvals.Open(dataDir); err != nil {
		return fail(fmt.Errorf("hub: open approvals: %w", err))
	}
	h.conductor = proto.ConductorState{State: "off"}
	h.spool = spool.NewManager(h.store, o.SpoolPoll, log, func(runID, code, msg string, off int64) {
		log.Warn("outbox line skipped", "run", runID, "code", code, "msg", msg, "offset", off)
		h.broker.PublishNotice("spool_warning", map[string]any{"run_id": runID, "code": code, "detail": msg})
	})
	h.spool.DrainAll() // A3: before the listener opens

	ln, err := listen(o, dataDir)
	if err != nil {
		return fail(err)
	}
	h.Port = ln.Addr().(*net.TCPAddr).Port
	h.URL = "http://" + net.JoinHostPort(ln.Addr().(*net.TCPAddr).IP.String(), strconv.Itoa(h.Port))
	if o.Port > 0 {
		if err := store.WriteFileAtomic(filepath.Join(dataDir, PortFile), []byte(strconv.Itoa(h.Port)+"\n"), 0o644); err != nil {
			log.Warn("cannot remember port", "err", err)
		}
	}

	if o.Repo != "" {
		h.runs, err = runs.New(runs.Options{Repo: o.Repo, DataDir: dataDir, Store: h.store,
			Config: func() config.Config { return h.cfg.Current().Config }, HubURL: func() string { return h.URL }, Logger: log})
		if err != nil {
			log.Warn("spawning disabled", "repo", o.Repo, "err", err)
			h.runs = nil
		}
	}
	if o.ConductorExe != "" && o.Repo != "" {
		h.cond, err = conductor.New(conductor.Options{DataDir: dataDir, Repo: o.Repo, Exe: o.ConductorExe, CLI: o.ConductorCLI,
			HubURL: func() string { return h.URL }, Config: func() config.Config { return h.cfg.Current().Config },
			Mint: h.MintConductorToken, Revoke: h.RevokeConductorToken, SetState: h.SetConductor, Bus: h.broker,
			Logger: log, ExtraEnv: o.ConductorEnv, WakeDelay: o.ConductorWake})
		if err != nil {
			return fail(fmt.Errorf("hub: conductor: %w", err))
		}
	}
	var condCtl api.ConductorCtl
	if h.cond != nil {
		condCtl = h.cond
	}
	handler := api.New(api.Deps{
		ConductorCtl: condCtl,
		Runs:         h.runs, Audit: h.audit, Approvals: h.approvals, OnApproval: o.OnApproval, Launcher: o.Launcher,
		FakeMerges: o.FakeMerges, CheckConductor: h.checkConductor,
		Conductor: func() proto.ConductorState { h.condMu.Lock(); defer h.condMu.Unlock(); return h.conductor }, Config: h.cfg, Touch: func() { h.lastAdm.Store(time.Now().UnixNano()) },
		Store: h.store, Bus: h.broker, AdminToken: h.AdminToken, Instance: ids.UUID(), Dev: o.Dev,
		Port: h.Port, URL: h.URL, Done: h.stopCh, Logger: log,
		Shutdown:  func() { _ = h.Close(context.Background()) },
		Dashboard: dashboard(),
	})
	h.srv = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	h.serveWG.Add(1)
	go func() {
		defer h.serveWG.Done()
		if err := h.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped", "err", err)
		}
	}()
	h.spool.Start()
	h.detWG.Add(1)
	go h.detectStalls()
	h.detWG.Add(1)
	go h.watchIdle()

	go func() {
		select {
		case <-ctx.Done():
			_ = h.Close(context.Background())
		case <-h.done:
		}
	}()
	if h.cond != nil {
		go h.cond.Resume() // a conductor that was running before the hub stopped picks up its session
	}
	log.Info("hub listening", "url", h.URL, "data_dir", dataDir)
	return h, nil
}

func loadOrCreateAdminToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	tok := ids.AdminToken()
	if err := store.WriteFileAtomic(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

func readPortFile(dataDir string) int {
	b, err := os.ReadFile(filepath.Join(dataDir, PortFile))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

func listen(o Options, dataDir string) (net.Listener, error) {
	var ports []int
	switch {
	case o.Port == 0:
		ports = []int{0}
	case !o.PortFallback:
		ports = []int{o.Port}
	default:
		if rem := readPortFile(dataDir); rem > 0 {
			ports = append(ports, rem)
		}
		for p := o.Port; p <= o.Port+portFallbackSpan && p <= 65535; p++ {
			if len(ports) == 0 || ports[0] != p {
				ports = append(ports, p)
			}
		}
	}
	var lastErr error
	for _, p := range ports {
		ln, err := net.Listen("tcp", net.JoinHostPort(strings.Trim(o.Bind, "[]"), strconv.Itoa(p)))
		if err == nil {
			return ln, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("hub: cannot listen on %s port %d: %w", o.Bind, o.Port, lastErr)
}

// stallTimeout is the heartbeat timeout for a run: fixed (tests) or live from the config, with
// the run's account override.
func (h *Hub) stallTimeout(r *store.Run, cfg *config.Config) time.Duration {
	if h.timeout > 0 {
		return h.timeout
	}
	sec := cfg.Hub.HeartbeatTimeoutS
	if acc, ok := cfg.Account(r.Record().AccountID); ok && acc.HeartbeatTimeoutS > 0 {
		sec = acc.HeartbeatTimeoutS
	}
	if sec <= 0 {
		sec = 120
	}
	return time.Duration(sec) * time.Second
}

// detectStalls periodically emits hub stalled events for silent runs (A4).
func (h *Hub) detectStalls() {
	defer h.detWG.Done()
	iv := 500 * time.Millisecond
	if h.timeout > 0 {
		iv = h.timeout / 4
		if iv < 10*time.Millisecond {
			iv = 10 * time.Millisecond
		}
		if iv > 5*time.Second {
			iv = 5 * time.Second
		}
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case now := <-t.C:
			func() {
				defer func() {
					if p := recover(); p != nil {
						h.log.Error("stall detector panic", "panic", fmt.Sprint(p))
					}
				}()
				cfg := h.cfg.Current().Config
				for _, r := range h.store.List() {
					r.CheckStalled(now, h.stallTimeout(r, &cfg))
				}
			}()
		}
	}
}

// idleAfter is the effective idle-stop delay (0 = never).
func (h *Hub) idleAfter() time.Duration {
	switch {
	case h.idleStop < 0:
		return 0
	case h.idleStop > 0:
		return h.idleStop
	}
	return time.Duration(h.cfg.Current().Config.Hub.IdleStopMin) * time.Minute
}

// watchIdle stops the hub once it has had no live runs, no SSE subscribers and no admin API
// request for the idle-stop delay.
func (h *Hub) watchIdle() {
	defer h.detWG.Done()
	iv := time.Second
	if h.idleStop > 0 && h.idleStop/4 < iv {
		if iv = h.idleStop / 4; iv < 10*time.Millisecond {
			iv = 10 * time.Millisecond
		}
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	lastBusy := time.Now()
	for {
		select {
		case <-h.stopCh:
			return
		case now := <-t.C:
			busy := h.broker.Subscribers() > 0
			if !busy {
				for _, r := range h.store.List() {
					if !r.Ended() {
						busy = true
						break
					}
				}
			}
			if busy {
				lastBusy = now
			}
			after := h.idleAfter()
			if after <= 0 {
				continue
			}
			last := lastBusy
			if adm := time.Unix(0, h.lastAdm.Load()); adm.After(last) {
				last = adm
			}
			if now.Sub(last) < after {
				continue
			}
			h.log.Info("idle: stopping hub", "after", after.String())
			h.broker.PublishNotice("idle_stop", map[string]any{"after_min": after.Minutes()})
			go func() { _ = h.Close(context.Background()) }()
			return
		}
	}
}

// Close shuts the hub down gracefully: ends SSE streams, stops the listener, watchers and
// timers, does a last outbox drain, syncs the stores and releases the lock. Idempotent.
func (h *Hub) Close(ctx context.Context) error {
	h.closeMu.Do(func() {
		defer close(h.done)
		if h.cond != nil {
			h.cond.Close()
		}
		close(h.stopCh)
		h.broker.Close()
		sctx, cancel := ctx, context.CancelFunc(func() {})
		if _, has := ctx.Deadline(); !has {
			sctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		}
		defer cancel()
		if err := h.srv.Shutdown(sctx); err != nil {
			_ = h.srv.Close()
			h.closeErr = err
		}
		h.serveWG.Wait()
		h.detWG.Wait()
		h.cfg.Stop()
		h.spool.Stop()
		h.spool.DrainAll()
		if err := h.audit.Close(); err != nil && h.closeErr == nil {
			h.closeErr = err
		}
		if err := h.store.Close(); err != nil && h.closeErr == nil {
			h.closeErr = err
		}
		h.lock.release()
		h.log.Info("hub stopped")
	})
	<-h.done
	return h.closeErr
}

// Store gives access to the run store (adapters and the mock engine append hub events).
func (h *Hub) Store() *store.Store { return h.store }

// SetConductor records what the conductor is doing and publishes a `conductor` notice.
func (h *Hub) SetConductor(c proto.ConductorState) {
	if c.Since == "" {
		c.Since = proto.NowTS()
	}
	h.condMu.Lock()
	h.conductor = c
	h.condMu.Unlock()
	h.broker.PublishNotice("conductor", map[string]any{"state": c.State, "account": c.Account, "model": c.Model, "since": c.Since})
}

// MintConductorToken starts a conductor session: it returns a new conductor token and
// invalidates the previous one. The token is held in memory only (a hub restart starts a new
// conductor session).
func (h *Hub) MintConductorToken() string {
	tok := ids.ConductorToken()
	sum := sha256.Sum256([]byte(tok))
	h.condTok.Store(&sum)
	return tok
}

// RevokeConductorToken ends the conductor session's access.
func (h *Hub) RevokeConductorToken() { h.condTok.Store(nil) }

func (h *Hub) checkConductor(tok string) bool {
	want := h.condTok.Load()
	if want == nil || tok == "" {
		return false
	}
	got := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// RequestApproval stores a pending approval, publishes an `approval` notice and audits it.
func (h *Hub) RequestApproval(kind, runID, title string, detail map[string]any, by string) (approvals.Approval, error) {
	ap, err := h.approvals.Add(kind, runID, title, detail, by)
	if err != nil {
		return ap, err
	}
	_ = h.audit.Append(audit.Entry{Actor: by, Action: "approval.request", Target: ap.ID, Detail: map[string]any{"kind": kind, "run_id": runID}})
	h.broker.PublishNotice("approval", map[string]any{"approval": ap})
	return ap, nil
}

// Done is closed after shutdown completes.
func (h *Hub) Done() <-chan struct{} { return h.done }

// dashboard serves the embedded web/dist build, or nil (the "not built" page) if absent.
func dashboard() http.Handler {
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return nil
	}
	return api.StaticDashboard(sub)
}

// Conductor gives access to the conductor (nil when the hub has none).
func (h *Hub) Conductor() *conductor.Manager { return h.cond }

// Runs gives access to the runs manager (nil when the hub has no target repo).
func (h *Hub) Runs() *runs.Manager { return h.runs }

// Config returns the current effective configuration.
func (h *Hub) Config() config.Config { return h.cfg.Current().Config }
