// Package conductor runs the headless conductor (GO_HUB_DESIGN §6.5, PLAN §4.2): a long-lived
// `claude -p` process with stream-json input and output on the conductor account, the Baton MCP
// server attached (`baton mcp` with a per-session conductor token) and the conductor prompt.
// Every line in and out is appended to <data>/conductor/history.jsonl; the session id is kept in
// session.json so a restarted hub resumes the same conversation. When the conductor is idle and
// an agent needs it, the hub sends it a short `[baton]` nudge (conductor.auto_wake).
package conductor

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/bus"
	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/proc"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/store"
)

//go:embed prompt.md
var Prompt string

// Files in <data>/conductor/.
const (
	Dir         = "conductor"
	HistoryFile = "history.jsonl"
	SessionFile = "session.json"
	MCPFile     = "mcp.json"
	PromptFile  = "prompt.md"
	StderrFile  = "stderr.log"
)

// States (proto.ConductorState.State).
const (
	StateOff      = "off"
	StateIdle     = "idle"
	StateThinking = "thinking"
	StateActing   = "acting"
)

// AllowedTools are the conductor's tools: Baton's MCP tools plus read-only project access.
// Everything else is denied without a prompt (nobody could answer one).
var AllowedTools = []string{"mcp__baton", "Read", "Grep", "Glob"}

// WakeTypes are the run events that wake an idle conductor (plus decided approvals).
var WakeTypes = []string{"question", "step_blocked", "done", "error", "stalled", "rate_limited", "exit"}

// WakeText is the hub's nudge.
const WakeText = "[baton] Agents need your attention. Call wait_for_events with timeout_s 0, handle what it returns, then reply in one or two lines."

// Errors.
var (
	ErrNoAccount   = errors.New("no conductor account: set conductor.account to a claude account in the config")
	ErrUnsupported = errors.New("only claude accounts can run the conductor for now")
)

// Options configures a Manager.
type Options struct {
	DataDir  string // project data dir
	Repo     string // working directory of the conductor
	Exe      string // baton binary, used as the MCP server command
	CLI      string // claude command; default "claude"
	HubURL   func() string
	Config   func() config.Config
	Mint     func() string // new conductor session token
	Revoke   func()
	SetState func(proto.ConductorState)
	Bus      *bus.Broker // auto-wake source; nil = never wake
	OnEntry  func(Entry) // called for every history entry (after it is written)
	Logger   *slog.Logger
	// Tests only.
	ExtraEnv  map[string]string
	WakeDelay time.Duration   // debounce before a nudge; default 2s
	Backoff   []time.Duration // restart delays after crashes; default 2s, 10s, 30s
}

// Entry is one history line.
type Entry struct {
	N     int             `json:"n"`
	TS    string          `json:"ts"`
	Dir   string          `json:"dir"`             // in | out | hub
	From  string          `json:"from,omitempty"`  // in: human | hub
	Text  string          `json:"text,omitempty"`  // in: message text; hub: detail
	Event string          `json:"event,omitempty"` // hub: started | exited | stopped | error
	Msg   json.RawMessage `json:"msg,omitempty"`   // out: the CLI's stream-json line
}

type session struct {
	SessionID string `json:"session_id"`
	Account   string `json:"account"`
	Used      bool   `json:"used"`   // at least one message sent (the CLI has stored the session)
	Wanted    bool   `json:"wanted"` // should run; kept across hub restarts
	CreatedAt string `json:"created_at"`
}

// resolved is what the conductor runs with.
type resolved struct {
	acct          config.Account
	model, effort string
}

// Manager owns the conductor process.
type Manager struct {
	o   Options
	dir string
	log *slog.Logger

	mu       sync.Mutex
	stdin    io.WriteCloser
	cmd      *exec.Cmd
	done     chan struct{} // closed when the current process has exited
	running  bool
	stopping bool
	closed   bool
	busy     bool // a turn is in progress
	pending  bool // something woke while busy
	wakeT    *time.Timer
	cur      resolved
	sess     session
	state    proto.ConductorState
	fails    []time.Time
	lastErr  string

	histMu sync.Mutex
	histN  int
	hist   *os.File

	stopWake chan struct{}
	stateCh  chan proto.ConductorState // SetState calls, in order

	subMu sync.Mutex
	subs  map[chan Update]struct{}
}

// Update is what subscribers get: a new history entry or a state change.
type Update struct {
	Entry *Entry                `json:"entry,omitempty"`
	State *proto.ConductorState `json:"state,omitempty"`
}

// Subscribe returns live updates until cancel is called. A subscriber that falls behind is
// dropped (its channel is closed); it should reconnect and replay from History.
func (m *Manager) Subscribe() (<-chan Update, func()) {
	ch := make(chan Update, 256)
	m.subMu.Lock()
	if m.subs == nil {
		m.subs = map[chan Update]struct{}{}
	}
	m.subs[ch] = struct{}{}
	m.subMu.Unlock()
	return ch, func() {
		m.subMu.Lock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
		m.subMu.Unlock()
	}
}

func (m *Manager) fanout(u Update) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- u:
		default:
			delete(m.subs, ch)
			close(ch)
		}
	}
}

// New opens the conductor's files. It does not start the process (see Resume / Start).
func New(o Options) (*Manager, error) {
	if o.CLI == "" {
		o.CLI = "claude"
	}
	if o.WakeDelay <= 0 {
		o.WakeDelay = 2 * time.Second
	}
	if o.Backoff == nil {
		o.Backoff = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second}
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	m := &Manager{o: o, dir: filepath.Join(o.DataDir, Dir), log: o.Logger, stopWake: make(chan struct{}),
		state: proto.ConductorState{State: StateOff}, stateCh: make(chan proto.ConductorState, 256)}
	go func() {
		for st := range m.stateCh {
			if o.SetState != nil {
				o.SetState(st)
			}
		}
	}()
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(m.dir, SessionFile)); err == nil {
		_ = json.Unmarshal(b, &m.sess)
	}
	n, err := countLines(filepath.Join(m.dir, HistoryFile))
	if err != nil {
		return nil, err
	}
	m.histN = n
	f, err := os.OpenFile(filepath.Join(m.dir, HistoryFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	m.hist = f
	if o.Bus != nil {
		go m.wakeLoop()
	}
	return m, nil
}

func countLines(p string) (int, error) {
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	last := 0
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e struct {
				N int `json:"n"`
			}
			if json.Unmarshal(line, &e) == nil && e.N > last {
				last = e.N
			}
		}
		if err != nil {
			return last, nil
		}
	}
}

// ---- history ----

func (m *Manager) record(e Entry) {
	m.histMu.Lock()
	m.histN++
	e.N = m.histN
	if e.TS == "" {
		e.TS = proto.NowTS()
	}
	b, _ := json.Marshal(e)
	if _, err := m.hist.Write(append(b, '\n')); err != nil {
		m.log.Warn("conductor history write failed", "err", err)
	}
	m.histMu.Unlock()
	m.fanout(Update{Entry: &e})
	if m.o.OnEntry != nil {
		m.o.OnEntry(e)
	}
}

// History returns entries with N > after; at most limit (the newest ones), 0 = 500.
func (m *Manager) History(after, limit int) ([]Entry, int, error) {
	if limit <= 0 {
		limit = 500
	}
	m.histMu.Lock()
	last := m.histN
	m.histMu.Unlock()
	f, err := os.Open(filepath.Join(m.dir, HistoryFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Entry{}, last, nil
		}
		return nil, last, err
	}
	defer f.Close()
	out := []Entry{}
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) > 1 && line[len(line)-1] == '\n' {
			var e Entry
			if json.Unmarshal(line, &e) == nil && e.N > after && e.N <= last {
				out = append(out, e)
				if len(out) > limit {
					out = out[1:]
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	return out, last, nil
}

// ---- state ----

func (m *Manager) setStateLocked(s string) {
	if m.state.State == s {
		return
	}
	m.state = proto.ConductorState{State: s, Account: m.cur.acct.ID, Model: m.cur.model, Since: proto.NowTS()}
	if s == StateOff {
		m.state.Account, m.state.Model = "", ""
	}
	if !m.closed || s == StateOff {
		select {
		case m.stateCh <- m.state:
		default: // never block under the lock; the next change brings it up to date
		}
	}
	st := m.state
	m.fanout(Update{State: &st})
}

// State returns what the conductor is doing.
func (m *Manager) State() proto.ConductorState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Info is the conductor's settings and status for the API.
type Info struct {
	State     proto.ConductorState `json:"state"`
	Running   bool                 `json:"running"`
	Wanted    bool                 `json:"wanted"`
	SessionID string               `json:"session_id,omitempty"`
	Account   string               `json:"account,omitempty"`
	Model     string               `json:"model,omitempty"`
	Effort    string               `json:"effort,omitempty"`
	LastError string               `json:"last_error,omitempty"`
	Problem   string               `json:"problem,omitempty"` // why it cannot start with the current config
	History   int                  `json:"history"`           // last history entry number
}

// Info reports the current status.
func (m *Manager) Info() Info {
	m.mu.Lock()
	in := Info{State: m.state, Running: m.running, Wanted: m.sess.Wanted, SessionID: m.sess.SessionID,
		LastError: m.lastErr}
	if m.running {
		in.Account, in.Model, in.Effort = m.cur.acct.ID, m.cur.model, m.cur.effort
	} else if r, err := m.resolve(); err == nil {
		in.Account, in.Model, in.Effort = r.acct.ID, r.model, r.effort
	} else {
		in.Problem = err.Error()
	}
	m.mu.Unlock()
	m.histMu.Lock()
	in.History = m.histN
	m.histMu.Unlock()
	return in
}

func (m *Manager) saveSessionLocked() {
	b, _ := json.MarshalIndent(m.sess, "", "  ")
	if err := store.WriteFileAtomic(filepath.Join(m.dir, SessionFile), b, 0o600); err != nil {
		m.log.Warn("conductor session write failed", "err", err)
	}
}

// ---- process ----

func (m *Manager) resolve() (resolved, error) {
	cfg := m.o.Config()
	c := cfg.Conductor
	var acct *config.Account
	if c.Account != "" {
		a, ok := cfg.Account(c.Account)
		if !ok {
			return resolved{}, fmt.Errorf("conductor account %q does not exist", c.Account)
		}
		acct = a
	} else {
		for i := range cfg.Accounts {
			if a := &cfg.Accounts[i]; a.Enabled && a.Kind == "claude" {
				acct = a
				break
			}
		}
		if acct == nil {
			return resolved{}, ErrNoAccount
		}
	}
	if acct.Kind != "claude" {
		return resolved{}, fmt.Errorf("%w (account %s is %s)", ErrUnsupported, acct.ID, acct.Kind)
	}
	if !acct.Enabled {
		return resolved{}, fmt.Errorf("conductor account %s is disabled", acct.ID)
	}
	return resolved{acct: *acct, model: c.Model, effort: c.Effort}, nil
}

// commandLine is the conductor command line. The prompt goes through a file: a multi-line argument
// would not survive a .cmd shim on Windows.
func commandLine(cli string, r resolved, mcpFile, promptFile, sessionID string, resume bool) []string {
	argv := []string{cli, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--mcp-config", mcpFile, "--strict-mcp-config",
		"--allowedTools", strings.Join(AllowedTools, ","), "--permission-prompts", "none",
		"--append-system-prompt-file", promptFile}
	if r.model != "" && r.model != "default" {
		argv = append(argv, "--model", r.model)
	}
	if r.effort != "" {
		argv = append(argv, "--effort", r.effort)
	}
	if resume {
		return append(argv, "--resume", sessionID)
	}
	return append(argv, "--session-id", sessionID)
}

// Resume starts the conductor if it was running when the hub last stopped.
func (m *Manager) Resume() {
	m.mu.Lock()
	want := m.sess.Wanted
	m.mu.Unlock()
	if want {
		if err := m.Start(); err != nil {
			m.log.Warn("conductor resume failed", "err", err)
		}
	}
}

// Start starts the conductor (no-op if running).
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked()
}

func (m *Manager) startLocked() error {
	if m.closed {
		return errors.New("hub is shutting down")
	}
	if m.running {
		return nil
	}
	r, err := m.resolve()
	if err != nil {
		m.lastErr = err.Error()
		return err
	}
	if m.sess.SessionID == "" || m.sess.Account != r.acct.ID {
		// a session lives in the account's config dir: another account means a new conversation
		m.sess = session{SessionID: ids.UUID(), Account: r.acct.ID, CreatedAt: proto.NowTS()}
	}
	tok := m.o.Mint()
	mcpPath := filepath.Join(m.dir, MCPFile)
	mcp := map[string]any{"mcpServers": map[string]any{"baton": map[string]any{
		"type": "stdio", "command": m.o.Exe, "args": []string{"mcp", "--url", m.o.HubURL()},
		"env": map[string]string{"BATON_CONDUCTOR_TOKEN": tok},
	}}}
	b, _ := json.MarshalIndent(mcp, "", "  ")
	if err := store.WriteFileAtomic(mcpPath, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", MCPFile, err)
	}
	promptPath := filepath.Join(m.dir, PromptFile)
	if err := store.WriteFileAtomic(promptPath, []byte(Prompt), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", PromptFile, err)
	}
	argv := commandLine(m.o.CLI, r, mcpPath, promptPath, m.sess.SessionID, m.sess.Used)
	acctEnv := map[string]string{}
	for k, v := range r.acct.Env {
		acctEnv[k] = v
	}
	env := proc.BuildEnv(nil, acctEnv, m.o.ExtraEnv)
	if m.o.Exe != "" {
		env = proc.PrependPath(env, filepath.Dir(m.o.Exe))
	}
	exe, err := exec.LookPath(argv[0])
	if err != nil {
		m.lastErr = fmt.Sprintf("%s not found on PATH", argv[0])
		return errors.New(m.lastErr)
	}
	cmd := exec.Command(exe, argv[1:]...)
	cmd.Dir, cmd.Env = m.o.Repo, env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if ef, err := os.OpenFile(filepath.Join(m.dir, StderrFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stderr = ef
		defer ef.Close() // the child holds its own handle
	}
	if err := cmd.Start(); err != nil {
		m.lastErr = err.Error()
		return fmt.Errorf("start conductor: %w", err)
	}
	m.cmd, m.stdin, m.running, m.stopping, m.busy, m.lastErr = cmd, stdin, true, false, false, ""
	m.done = make(chan struct{})
	m.cur = r
	m.sess.Wanted = true
	m.saveSessionLocked()
	m.setStateLocked(StateIdle)
	m.record(Entry{Dir: "hub", Event: "started", Text: fmt.Sprintf("account %s, model %s, effort %s, session %s (resume %v)",
		r.acct.ID, r.model, r.effort, m.sess.SessionID, m.sess.Used)})
	go m.readLoop(stdout, m.done)
	go m.waitLoop(cmd, m.done)
	return nil
}

func (m *Manager) readLoop(stdout io.Reader, done chan struct{}) {
	r := bufio.NewReaderSize(stdout, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if t := strings.TrimSpace(string(line)); t != "" {
			m.handleOut([]byte(t))
		}
		if err != nil {
			return
		}
	}
}

func (m *Manager) handleOut(line []byte) {
	var ev struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
		Message   struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
	}
	raw := json.RawMessage(line)
	if json.Unmarshal(line, &ev) != nil {
		raw, _ = json.Marshal(map[string]string{"type": "raw", "text": string(line)})
	}
	m.record(Entry{Dir: "out", Msg: raw})
	m.mu.Lock()
	defer m.mu.Unlock()
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" && ev.SessionID != "" && ev.SessionID != m.sess.SessionID {
			m.sess.SessionID = ev.SessionID
			m.saveSessionLocked()
		}
	case "assistant":
		st := StateThinking
		for _, c := range ev.Message.Content {
			if c.Type == "tool_use" {
				st = StateActing
			}
		}
		m.setStateLocked(st)
	case "result":
		m.busy = false
		m.setStateLocked(StateIdle)
		if m.pending {
			m.pending = false
			m.armWakeLocked()
		}
	}
}

func (m *Manager) waitLoop(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	m.mu.Lock()
	expected := m.stopping || m.closed
	m.running, m.busy, m.cmd, m.stdin = false, false, nil, nil
	close(done)
	m.setStateLocked(StateOff)
	if m.o.Revoke != nil {
		m.o.Revoke()
	}
	if expected {
		m.mu.Unlock()
		m.record(Entry{Dir: "hub", Event: "stopped", Text: fmt.Sprintf("exit code %d", code)})
		return
	}
	now := time.Now()
	recent := []time.Time{}
	for _, t := range m.fails {
		if now.Sub(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	m.fails = append(recent, now)
	n := len(m.fails)
	m.lastErr = fmt.Sprintf("conductor exited unexpectedly (code %d); see %s", code, filepath.Join(m.dir, StderrFile))
	m.mu.Unlock()
	m.record(Entry{Dir: "hub", Event: "exited", Text: m.lastErr})
	if n > len(m.o.Backoff) {
		m.record(Entry{Dir: "hub", Event: "error", Text: "conductor keeps exiting; not restarting it. Fix the cause, then start it again."})
		return
	}
	delay := m.o.Backoff[n-1]
	time.AfterFunc(delay, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed || m.running || !m.sess.Wanted {
			return
		}
		if err := m.startLocked(); err != nil {
			m.log.Warn("conductor restart failed", "err", err)
		}
	})
}

// Send writes a user message to the conductor, starting it if needed. from is human or hub.
func (m *Manager) Send(text, from string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty message")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running && !m.busy {
		m.applyConfigLocked()
	}
	if err := m.startLocked(); err != nil {
		return err
	}
	msg := map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": []map[string]any{{"type": "text", "text": text}}}}
	b, _ := json.Marshal(msg)
	m.record(Entry{Dir: "in", From: from, Text: text}) // before the write: replies can be instant
	if _, err := m.stdin.Write(append(b, '\n')); err != nil {
		m.record(Entry{Dir: "hub", Event: "error", Text: "message not delivered: " + err.Error()})
		return fmt.Errorf("conductor is not accepting input: %w", err)
	}
	m.busy = true
	if !m.sess.Used {
		m.sess.Used = true
		m.saveSessionLocked()
	}
	m.setStateLocked(StateThinking)
	return nil
}

// applyConfigLocked restarts an idle conductor whose account/model/effort changed in the config
// (same account: the conversation is resumed; another account: a new one starts).
func (m *Manager) applyConfigLocked() {
	r, err := m.resolve()
	if err != nil || (r.acct.ID == m.cur.acct.ID && r.model == m.cur.model && r.effort == m.cur.effort) {
		return
	}
	m.record(Entry{Dir: "hub", Event: "stopped", Text: "config changed: restarting with the new settings"})
	m.stopProcLocked()
}

// stopProcLocked ends the process (stdin closed, then killed after 5 s) and waits for it. It
// unlocks m.mu while waiting.
func (m *Manager) stopProcLocked() {
	if !m.running {
		return
	}
	m.stopping = true
	done, cmd := m.done, m.cmd
	if m.stdin != nil {
		_ = m.stdin.Close()
	}
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
	m.mu.Lock()
}

// Stop stops the conductor; it stays off after a hub restart.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sess.Wanted = false
	m.saveSessionLocked()
	m.stopProcLocked()
}

// Reset stops the conductor and forgets its conversation (the next start is a new session).
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopProcLocked()
	m.sess = session{}
	m.saveSessionLocked()
	m.record(Entry{Dir: "hub", Event: "stopped", Text: "conversation reset"})
}

// Close stops the process for a hub shutdown; a wanted conductor resumes on the next start.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stopWake)
	if m.wakeT != nil {
		m.wakeT.Stop()
	}
	m.stopProcLocked()
	m.mu.Unlock()
	m.histMu.Lock()
	_ = m.hist.Close()
	m.histMu.Unlock()
}

// ---- auto-wake ----

func wakes(it bus.Item) bool {
	if it.Notice != nil {
		ap, ok := it.Notice.Data["approval"].(approvals.Approval)
		return it.Notice.Type == "approval" && ok && ap.Status != approvals.Pending
	}
	return slices.Contains(WakeTypes, it.Ev.Type)
}

func (m *Manager) wakeLoop() {
	sub, _, _ := m.o.Bus.Subscribe(m.o.Bus.LastID(), true)
	defer sub.Close()
	for {
		select {
		case it := <-sub.C:
			if !wakes(it) || !m.o.Config().Conductor.AutoWake {
				continue
			}
			m.mu.Lock()
			if m.running {
				if m.busy {
					m.pending = true
				} else {
					m.armWakeLocked()
				}
			}
			m.mu.Unlock()
		case <-sub.Dropped:
			sub, _, _ = m.o.Bus.Subscribe(m.o.Bus.LastID(), true)
		case <-m.stopWake:
			return
		case <-m.o.Bus.Closed:
			return
		}
	}
}

func (m *Manager) armWakeLocked() {
	if m.wakeT != nil {
		return // a nudge is already scheduled
	}
	m.wakeT = time.AfterFunc(m.o.WakeDelay, func() {
		m.mu.Lock()
		m.wakeT = nil
		if !m.running || m.closed {
			m.mu.Unlock()
			return
		}
		if m.busy {
			m.pending = true
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		if err := m.Send(WakeText, "hub"); err != nil {
			m.log.Warn("conductor wake failed", "err", err)
		}
	})
}
