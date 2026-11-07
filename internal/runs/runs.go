// Package runs creates agent runs: account selection, identity, git worktree, brief and the
// BATON_* environment. It does not start any process (adapters do, Phase 5).
package runs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/briefing"
	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/store"
)

// Options configures a Manager.
type Options struct {
	Repo, DataDir string
	Store         *store.Store
	Config        func() config.Config
	HubURL        func() string
	Logger        *slog.Logger
}

// Scope is what the agent may touch.
type Scope struct {
	Paths     []string `json:"paths"`
	Forbidden []string `json:"forbidden"`
	AskBefore []string `json:"ask_before"`
}

// SpawnRequest is the body of spawn_agent / POST /api/agents.
type SpawnRequest struct {
	Account    string           `json:"account"`
	Role       string           `json:"role"`
	Kind       string           `json:"kind"` // optional; default account.kind
	Model      string           `json:"model"`
	Effort     string           `json:"effort"`
	Callsign   string           `json:"callsign"`
	Task       string           `json:"task"`
	Plan       []proto.PlanStep `json:"plan"`
	Scope      Scope            `json:"scope"`
	BaseBranch string           `json:"base_branch"`
	DryRun     bool             `json:"dry_run"`
	Actor      string           `json:"actor"` // human | conductor; default human
}

// SpawnResult describes the new (or, for a dry run, would-be) run.
type SpawnResult struct {
	RunID      string            `json:"run_id"`
	AgentID    string            `json:"agent_id"`
	Callsign   string            `json:"callsign"`
	AccountID  string            `json:"account_id"`
	Role       string            `json:"role"`
	Kind       string            `json:"kind"`
	Model      string            `json:"model"`
	Branch     string            `json:"branch"`
	BaseBranch string            `json:"base_branch"`
	Worktree   string            `json:"worktree"`
	RunDir     string            `json:"run_dir"`
	Brief      string            `json:"brief"`
	Env        map[string]string `json:"env,omitempty"` // BATON_* incl. token; omitted for dry run
	DryRun     bool              `json:"dry_run"`
}

// Manager spawns runs and manages their worktrees.
type Manager struct {
	o   Options
	log *slog.Logger
	mu  sync.Mutex // serialises Spawn so caps cannot be exceeded by races
	// mergeMu serialises merges (two approvals of the same run must not both merge).
	mergeMu sync.Mutex
}

// New checks that the repo is a git repository.
func New(o Options) (*Manager, error) {
	if o.Store == nil || o.Config == nil {
		return nil, fmt.Errorf("runs: Store and Config are required")
	}
	if o.HubURL == nil {
		o.HubURL = func() string { return "" }
	}
	if o.DataDir == "" {
		o.DataDir = o.Store.DataDir()
	}
	if !gitx.IsRepo(o.Repo) {
		return nil, proto.NewError(proto.CodeNotARepo, o.Repo+" is not a git repository")
	}
	top, err := gitx.TopLevel(o.Repo)
	if err != nil {
		return nil, proto.NewError(proto.CodeNotARepo, err.Error())
	}
	o.Repo = top
	l := o.Logger
	if l == nil {
		l = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Manager{o: o, log: l}, nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Live counts runs that have not ended on the account ("" = all accounts).
func (m *Manager) Live(accountID string) int {
	n := 0
	for _, r := range m.o.Store.List() {
		if r.Ended() {
			continue
		}
		if accountID == "" || r.Record().AccountID == accountID {
			n++
		}
	}
	return n
}

func perr(code, format string, a ...any) *proto.Error {
	return proto.NewError(code, fmt.Sprintf(format, a...))
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func validate(req *SpawnRequest) *proto.Error {
	req.Role = strings.TrimSpace(req.Role)
	req.Task = strings.TrimSpace(req.Task)
	if req.Role == "" {
		return perr(proto.CodeInvalidPayload, "role is required")
	}
	if req.Task == "" {
		return perr(proto.CodeInvalidPayload, "task is required")
	}
	switch req.Actor {
	case "":
		req.Actor = "human"
	case "human", "conductor":
	default:
		return perr(proto.CodeInvalidPayload, "actor must be human or conductor")
	}
	seen := map[string]bool{}
	for i, s := range req.Plan {
		if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Title) == "" {
			return perr(proto.CodeInvalidPayload, "plan step %d needs an id and a title", i+1)
		}
		if seen[s.ID] {
			return perr(proto.CodeInvalidPayload, "duplicate plan step id %q", s.ID)
		}
		seen[s.ID] = true
	}
	return nil
}

// selectAccount applies spec 4.2 in strict order. It returns the account and the model to use.
func selectAccount(cfg config.Config, req SpawnRequest, live func(string) int) (*config.Account, string, *proto.Error) {
	id := strings.TrimSpace(req.Account)
	if id == "" {
		id = cfg.Routing[req.Role]
	}
	if id == "" {
		var ok []string
		for _, a := range cfg.Accounts {
			if a.Enabled && (len(a.Roles) == 0 || contains(a.Roles, req.Role)) {
				ok = append(ok, a.ID)
			}
		}
		list := "none configured"
		if len(ok) > 0 {
			list = strings.Join(ok, ", ")
		}
		return nil, "", perr(proto.CodeAccountRequired,
			"no account given and no routing for role %q; choose one of: %s", req.Role, list)
	}
	acc, found := cfg.Account(id)
	if !found {
		return nil, "", perr(proto.CodeUnknownAccount, "unknown account %q", id)
	}
	if !acc.Enabled {
		return nil, "", perr(proto.CodeAccountDisabled, "account %q is disabled", id)
	}
	if len(acc.Roles) > 0 && !contains(acc.Roles, req.Role) {
		return nil, "", perr(proto.CodeRoleNotAllowed, "account %q does not allow role %q (allowed: %s)",
			id, req.Role, strings.Join(acc.Roles, ", "))
	}
	model := req.Model
	if len(acc.Models) > 0 {
		if model == "" {
			model = acc.Models[0]
		} else if !contains(acc.Models, model) {
			return nil, "", perr(proto.CodeModelNotAllowed, "account %q does not allow model %q (allowed: %s)",
				id, model, strings.Join(acc.Models, ", "))
		}
	}
	limit := acc.MaxConcurrent
	if limit <= 0 {
		limit = 1
	}
	if n := live(id); n >= limit {
		return nil, "", perr(proto.CodeAccountBusy, "account %q is at its limit (%d/%d running)", id, n, limit)
	}
	if cfg.Hub.Mode == config.ModeAuto && req.Actor == "conductor" && cfg.Hub.MaxAgentsAuto > 0 {
		if n := live(""); n >= cfg.Hub.MaxAgentsAuto {
			return nil, "", perr(proto.CodeMaxAgents, "auto mode allows at most %d live agents (%d running)",
				cfg.Hub.MaxAgentsAuto, n)
		}
	}
	return acc, model, nil
}

func kindPrefix(kind string) string {
	switch kind {
	case "claude":
		return "cc"
	case "codex":
		return "cx"
	case "opencode":
		return "oc"
	}
	return "gn"
}

func accountPart(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 && i < len(id)-1 {
		return id[i+1:]
	}
	return id
}

func (m *Manager) seqFile() string { return filepath.Join(m.o.DataDir, "agent.seq") }

func (m *Manager) peekSeq() int {
	b, err := os.ReadFile(m.seqFile())
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func (m *Manager) commitSeq(n int) error {
	if err := os.MkdirAll(m.o.DataDir, 0o755); err != nil {
		return err
	}
	return store.WriteFileAtomic(m.seqFile(), []byte(strconv.Itoa(n)+"\n"), 0o644)
}

func (m *Manager) liveCallsigns() map[string]bool {
	used := map[string]bool{}
	for _, r := range m.o.Store.List() {
		if !r.Ended() {
			used[strings.ToLower(r.Record().Callsign)] = true
		}
	}
	return used
}

func pickCallsign(want string, used map[string]bool, seq int) (string, *proto.Error) {
	if want = strings.TrimSpace(want); want != "" {
		if used[strings.ToLower(want)] {
			return "", perr(proto.CodeCallsignTaken, "callsign %q is already used by a live agent", want)
		}
		return want, nil
	}
	for i := 0; i < len(callsigns); i++ {
		c := callsigns[(seq+i)%len(callsigns)]
		if !used[strings.ToLower(c)] {
			return c, nil
		}
	}
	for i := 2; ; i++ { // every name taken: suffix a number
		c := fmt.Sprintf("%s-%d", callsigns[seq%len(callsigns)], i)
		if !used[strings.ToLower(c)] {
			return c, nil
		}
	}
}

func timeouts(acc *config.Account, cfg config.Config) map[string]string {
	ask := cfg.Hub.AskTimeoutS
	if acc.AskTimeoutS > 0 {
		ask = acc.AskTimeoutS
	}
	if ask <= 0 {
		ask = 900
	}
	poll := cfg.Hub.PollIntervalS
	if acc.PollIntervalS > 0 {
		poll = acc.PollIntervalS
	}
	if poll <= 0 {
		poll = 2
	}
	hb := cfg.Hub.HeartbeatTimeoutS
	if acc.HeartbeatTimeoutS > 0 {
		hb = acc.HeartbeatTimeoutS
	}
	if hb <= 0 {
		hb = 120
	}
	every := hb / 4 // heartbeat well inside the stall timeout
	if every < 5 {
		every = 5
	}
	return map[string]string{
		"BATON_ASK_TIMEOUT_S":        strconv.Itoa(ask),
		"BATON_POLL_INTERVAL_S":      strconv.FormatFloat(poll, 'f', -1, 64),
		"BATON_HEARTBEAT_INTERVAL_S": strconv.Itoa(every),
	}
}

// Spawn validates, selects the account, and creates the run with its worktree and brief.
func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (*SpawnResult, *proto.Error) {
	if e := validate(&req); e != nil {
		return nil, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg := m.o.Config()
	acc, model, e := selectAccount(cfg, req, m.Live)
	if e != nil {
		return nil, e
	}
	kind := acc.Kind
	if kind == "" {
		kind = "generic"
	}
	// The account's credentials and isolation env belong to its own kind; running another kind
	// with them could use the user's global login (never on a Claude subscription).
	if req.Kind != "" && req.Kind != kind {
		return nil, perr(proto.CodeInvalidPayload, "kind %q does not match account %q (kind %q); pick an account of that kind instead of overriding it", req.Kind, acc.ID, kind)
	}
	seq := m.peekSeq() + 1
	agentID := fmt.Sprintf("%s-%s-%04d", kindPrefix(kind), accountPart(acc.ID), seq)
	callsign, e := pickCallsign(req.Callsign, m.liveCallsigns(), seq)
	if e != nil {
		return nil, e
	}
	base := strings.TrimSpace(req.BaseBranch)
	if base == "" {
		b, err := gitx.CurrentBranch(m.o.Repo)
		if err != nil {
			return nil, perr(proto.CodeGit, "cannot determine base branch: %v", err)
		}
		base = b
	}
	runID := ids.RunID()
	runDir := paths.RunDir(m.o.DataDir, runID)
	worktree := filepath.Join(m.o.DataDir, "worktrees", agentID)
	branch := "baton/" + agentID

	label := acc.Label
	if label == "" {
		label = acc.ID
	}
	data := briefing.Data{
		Callsign: callsign, AgentID: agentID, Role: req.Role, Kind: kind, Model: model,
		AccountLabel: label, Branch: branch, Worktree: worktree, Task: req.Task,
		Paths: req.Scope.Paths, Forbidden: req.Scope.Forbidden, AskBefore: req.Scope.AskBefore,
		Sandboxed: acc.Permissions.Mode == config.ModeSandboxed,
	}
	if ck, ok := cfg.Kinds[kind]; ok && ck.Notes != "" {
		data.KindNotes = ck.Notes
	}
	for _, s := range req.Plan {
		data.Steps = append(data.Steps, briefing.Step{ID: s.ID, Title: s.Title, Acceptance: s.Acceptance})
	}
	res := &SpawnResult{
		RunID: runID, AgentID: agentID, Callsign: callsign, AccountID: acc.ID, Role: req.Role,
		Kind: kind, Model: model, Branch: branch, BaseBranch: base, Worktree: worktree,
		RunDir: runDir, DryRun: req.DryRun,
	}
	if req.DryRun {
		brief, err := briefing.Render(data)
		if err != nil {
			return nil, perr(proto.CodeInternal, "%v", err)
		}
		res.Brief = brief
		return res, nil
	}

	// Consume the id first so it is never reused, even if a later step fails.
	if err := m.commitSeq(seq); err != nil {
		return nil, perr(proto.CodeInternal, "cannot persist agent counter: %v", err)
	}
	if _, err := gitx.Run(ctx, m.o.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return nil, perr(proto.CodeGit, "branch %s already exists", branch)
	}
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		return nil, perr(proto.CodeInternal, "%v", err)
	}
	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	fail := func(code, format string, a ...any) (*SpawnResult, *proto.Error) {
		rollback()
		return nil, perr(code, format, a...)
	}
	removeWorktree := func() {
		bg := context.Background()
		_, _ = gitx.Run(bg, m.o.Repo, "worktree", "remove", "--force", worktree)
		_ = os.RemoveAll(worktree)
		_, _ = gitx.Run(bg, m.o.Repo, "worktree", "prune")
		_, _ = gitx.Run(bg, m.o.Repo, "branch", "-D", branch)
	}

	if _, err := gitx.Run(ctx, m.o.Repo, "worktree", "add", "-b", branch, worktree, base); err != nil {
		removeWorktree() // git can leave a half-made branch or directory behind
		return nil, perr(proto.CodeGit, "%v", err)
	}
	undo = append(undo, removeWorktree)

	// Codex reads AGENTS.md: write it only when the repo has none.
	excludes := []string{"BATON_BRIEF.md"}
	agentsPath := filepath.Join(worktree, "AGENTS.md")
	writeAgents := false
	if kind == "codex" {
		if _, err := os.Stat(agentsPath); err == nil {
			data.ExtraNote = "This repo already has an AGENTS.md; Baton did not change it. The rules are in BATON_BRIEF.md."
		} else {
			writeAgents = true
			// Not added to info/exclude: that file is shared with the main checkout and would hide
			// a real AGENTS.md there. The brief tells the agent not to commit it instead.
			data.ExtraNote = "Baton wrote AGENTS.md in this worktree for you; do not commit it."
		}
	}
	brief, err := briefing.Render(data)
	if err != nil {
		return fail(proto.CodeInternal, "%v", err)
	}
	res.Brief = brief
	if err := os.WriteFile(filepath.Join(worktree, "BATON_BRIEF.md"), []byte(brief), 0o644); err != nil {
		return fail(proto.CodeInternal, "write brief: %v", err)
	}
	if writeAgents {
		if err := os.WriteFile(agentsPath, []byte(brief), 0o644); err != nil {
			return fail(proto.CodeInternal, "write AGENTS.md: %v", err)
		}
	}
	if err := m.exclude(ctx, worktree, excludes); err != nil {
		return fail(proto.CodeGit, "exclude brief: %v", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fail(proto.CodeInternal, "%v", err)
	}
	undo = append(undo, func() { _ = os.RemoveAll(runDir) })
	if err := os.WriteFile(filepath.Join(runDir, "BATON_BRIEF.md"), []byte(brief), 0o644); err != nil {
		return fail(proto.CodeInternal, "write brief: %v", err)
	}
	// Create last: the store has no delete, so nothing may fail after it.
	plan := req.Plan
	if plan == nil {
		plan = []proto.PlanStep{}
	}
	_, token, err := m.o.Store.Create(store.CreateOpts{
		RunID: runID, AgentID: agentID, Callsign: callsign, AccountID: acc.ID, Role: req.Role,
		Kind: kind, Model: model, Effort: req.Effort, Task: req.Task, Repo: m.o.Repo, Branch: branch,
		Base: base, Worktree: worktree, Actor: req.Actor, Plan: plan,
	})
	if err != nil {
		return fail(proto.CodeInternal, "create run: %v", err)
	}
	env := map[string]string{
		"BATON_URL":      m.o.HubURL(),
		"BATON_TOKEN":    token,
		"BATON_RUN_ID":   runID,
		"BATON_AGENT_ID": agentID,
		"BATON_RUN_DIR":  runDir,
		"BATON_BRIEF":    filepath.Join(worktree, "BATON_BRIEF.md"),
		"BATON_MODEL":    model,
	}
	for k, v := range timeouts(acc, cfg) {
		env[k] = v
	}
	res.Env = env
	m.log.Info("run created", "run", runID, "agent", agentID, "account", acc.ID, "branch", branch)
	return res, nil
}

// exclude adds names to the worktree's info/exclude so they are never committed.
func (m *Manager) exclude(ctx context.Context, worktree string, names []string) error {
	p, err := gitx.Run(ctx, worktree, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	p = filepath.FromSlash(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(worktree, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	old, _ := os.ReadFile(p)
	var add strings.Builder
	if len(old) > 0 && !strings.HasSuffix(string(old), "\n") {
		add.WriteString("\n")
	}
	have := strings.Split(string(old), "\n")
	for _, n := range names {
		if !contains(have, n) {
			add.WriteString(n + "\n")
		}
	}
	if add.Len() == 0 {
		return nil
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(add.String())
	return err
}

// DiffStat returns "<shortstat of base...HEAD>; N uncommitted".
func (m *Manager) DiffStat(ctx context.Context, runID string) (string, error) {
	r := m.o.Store.Get(runID)
	if r == nil {
		return "", fmt.Errorf("unknown run %s", runID)
	}
	rec := r.Record()
	if rec.Worktree == "" {
		return "", fmt.Errorf("run %s has no worktree", runID)
	}
	if _, err := os.Stat(rec.Worktree); err != nil {
		return "", fmt.Errorf("worktree gone: %w", err)
	}
	stat, err := gitx.Run(ctx, rec.Worktree, "diff", "--shortstat", rec.BaseBranch+"...HEAD")
	if err != nil {
		return "", err
	}
	if stat == "" {
		stat = "no changes"
	}
	unc, err := m.uncommitted(ctx, rec.Worktree)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s; %d uncommitted", stat, len(unc)), nil
}

// RemoveWorktree removes the run's worktree (and optionally its branch).
func (m *Manager) RemoveWorktree(ctx context.Context, runID string, deleteBranch bool) error {
	r := m.o.Store.Get(runID)
	if r == nil {
		return fmt.Errorf("unknown run %s", runID)
	}
	rec := r.Record()
	if rec.Worktree == "" {
		return fmt.Errorf("run %s has no worktree", runID)
	}
	if _, err := os.Stat(rec.Worktree); err == nil {
		if _, err := gitx.Run(ctx, m.o.Repo, "worktree", "remove", "--force", rec.Worktree); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(rec.Worktree)
	if _, err := gitx.Run(ctx, m.o.Repo, "worktree", "prune"); err != nil {
		return err
	}
	if deleteBranch && rec.Branch != "" {
		if _, err := gitx.Run(ctx, m.o.Repo, "branch", "-D", rec.Branch); err != nil {
			return err
		}
	}
	return nil
}

// CleanupDue lists ended runs whose worktree still exists and the cleanup policy says to remove.
func (m *Manager) CleanupDue(now time.Time) []string {
	w := m.o.Config().Hub.Worktrees
	var out []string
	if w.Cleanup != config.CleanupRemoveOnMerge && w.Cleanup != config.CleanupRemoveAfter {
		return out
	}
	days := w.Days
	if days <= 0 {
		days = 14
	}
	for _, r := range m.o.Store.List() {
		if !r.Ended() {
			continue
		}
		rec := r.Record()
		if rec.Worktree == "" {
			continue
		}
		if _, err := os.Stat(rec.Worktree); err != nil {
			continue
		}
		switch w.Cleanup {
		case config.CleanupRemoveAfter:
			t, err := time.Parse(time.RFC3339Nano, rec.EndedAt)
			if err != nil {
				continue
			}
			if !now.Before(t.Add(time.Duration(days) * 24 * time.Hour)) {
				out = append(out, rec.RunID)
			}
		case config.CleanupRemoveOnMerge:
			if string(rec.Extra["merged"]) == "true" {
				out = append(out, rec.RunID)
			}
		}
	}
	return out
}

// Launcher starts and kills the agent process of a run (the real one in Phase 5, a fake one in
// `baton mock`). Kill appends the run's `killed` event itself.
type Launcher interface {
	Start(res SpawnResult) error
	Kill(runID string) error
}
