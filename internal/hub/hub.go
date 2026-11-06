// Package hub wires the store, spool tailer, event bus and HTTP API into one running hub.
package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/api"
	"github.com/whosabhiraj/baton/internal/bus"
	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/redact"
	"github.com/whosabhiraj/baton/internal/spool"
	"github.com/whosabhiraj/baton/internal/store"
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
	HeartbeatTimeout time.Duration // default 120s; stalled detection
	Logger           *slog.Logger  // nil = discard
}

// Hub is a running hub.
type Hub struct {
	URL        string
	AdminToken string
	DataDir    string
	Port       int

	log      *slog.Logger
	lock     *fileLock
	store    *store.Store
	broker   *bus.Broker
	spool    *spool.Manager
	srv      *http.Server
	timeout  time.Duration
	stopCh   chan struct{} // closed first on shutdown: ends SSE streams and background loops
	detWG    sync.WaitGroup
	serveWG  sync.WaitGroup
	done     chan struct{}
	closeMu  sync.Once
	closeErr error
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
	if o.HeartbeatTimeout <= 0 {
		o.HeartbeatTimeout = 120 * time.Second
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
		stopCh: make(chan struct{}), done: make(chan struct{})}
	fail := func(err error) (*Hub, error) {
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
	h.store, err = store.Open(dataDir, store.Options{Logger: log, Publish: func(ev proto.StoredEvent) { h.broker.Publish(ev) }})
	if err != nil {
		return fail(fmt.Errorf("hub: open store: %w", err))
	}
	h.spool = spool.NewManager(h.store, o.SpoolPoll, log, func(runID, code, msg string, off int64) {
		log.Warn("outbox line skipped", "run", runID, "code", code, "msg", msg, "offset", off)
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

	handler := api.New(api.Deps{
		Store: h.store, Bus: h.broker, AdminToken: h.AdminToken, Instance: ids.UUID(), Dev: o.Dev,
		Port: h.Port, URL: h.URL, Done: h.stopCh, Logger: log,
		Shutdown: func() { _ = h.Close(context.Background()) },
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

	go func() {
		select {
		case <-ctx.Done():
			_ = h.Close(context.Background())
		case <-h.done:
		}
	}()
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

// detectStalls periodically emits hub stalled events for silent runs (A4).
func (h *Hub) detectStalls() {
	defer h.detWG.Done()
	iv := h.timeout / 4
	if iv < 10*time.Millisecond {
		iv = 10 * time.Millisecond
	}
	if iv > 5*time.Second {
		iv = 5 * time.Second
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
				for _, r := range h.store.List() {
					r.CheckStalled(now, h.timeout)
				}
			}()
		}
	}
}

// Close shuts the hub down gracefully: ends SSE streams, stops the listener, watchers and
// timers, does a last outbox drain, syncs the stores and releases the lock. Idempotent.
func (h *Hub) Close(ctx context.Context) error {
	h.closeMu.Do(func() {
		defer close(h.done)
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
		h.spool.Stop()
		h.spool.DrainAll()
		if err := h.store.Close(); err != nil && h.closeErr == nil {
			h.closeErr = err
		}
		h.lock.release()
		h.log.Info("hub stopped")
	})
	<-h.done
	return h.closeErr
}

// Done is closed after shutdown completes.
func (h *Hub) Done() <-chan struct{} { return h.done }
