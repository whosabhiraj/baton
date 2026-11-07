// Package store is the per-run durable store: run.json (identity and control), events.jsonl
// (the append-only truth), inbox files, and the state derived from the event log.
package store

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
)

// Options configures a Store.
type Options struct {
	Logger *slog.Logger // nil = discard
	// Publish is called (with the run lock held, in seq order) for every stored event. It must
	// not block and must not call back into the run.
	Publish func(proto.StoredEvent)
}

// Store holds all runs of one project data directory.
type Store struct {
	dir  string
	opts Options
	log  *slog.Logger

	mu   sync.RWMutex
	runs map[string]*Run
}

// CreateOpts describes a new run.
type CreateOpts struct {
	RunID     string // "" = generated
	AgentID   string
	Callsign  string
	AccountID string
	Role      string
	Kind      string
	Model     string
	Effort    string
	Task      string
	Repo      string
	Branch    string
	Base      string // base branch / commit
	Worktree  string
	Actor     string
	Plan      []proto.PlanStep
	Token     string // "" = generated
}

// Open loads every run found under <dataDir>/runs, repairing torn event-log tails and
// missing to_agent events.
func Open(dataDir string, o Options) (*Store, error) {
	s := &Store{dir: dataDir, opts: o, log: o.Logger, runs: map[string]*Run{}}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := paths.EnsureDataDir(dataDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(paths.RunsDir(dataDir), 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(paths.RunsDir(dataDir))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(paths.RunsDir(dataDir), e.Name())
		if _, err := os.Stat(filepath.Join(dir, paths.RunRecordFile)); err != nil {
			continue
		}
		r, err := s.loadRun(dir)
		if err != nil {
			s.log.Warn("skipping unreadable run", "dir", dir, "err", err)
			continue
		}
		s.runs[r.id] = r
	}
	return s, nil
}

// DataDir returns the data directory the store was opened on.
func (s *Store) DataDir() string { return s.dir }

// Close syncs and closes every run. The store must not be used afterwards.
func (s *Store) Close() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var first error
	for _, r := range s.runs {
		if err := r.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Get returns the run with the given id, or nil.
func (s *Store) Get(id string) *Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runs[id]
}

// List returns all runs ordered by creation time (then id).
func (s *Store) List() []*Run {
	s.mu.RLock()
	out := make([]*Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].created != out[j].created {
			return out[i].created < out[j].created
		}
		return out[i].id < out[j].id
	})
	return out
}

// Summaries returns the RunSummary of every run (GET /api/state).
func (s *Store) Summaries() []Summary {
	runs := s.List()
	out := make([]Summary, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.Summary())
	}
	return out
}

// RunByToken returns the run whose token hash matches tok (revoked or not), or nil.
func (s *Store) RunByToken(tok string) *Run {
	if tok == "" {
		return nil
	}
	var found *Run
	for _, r := range s.List() { // no early exit: constant work per request
		if r.tokenMatches(tok) && found == nil {
			found = r
		}
	}
	return found
}

// Create makes a new run (run.json, inbox dir, empty event log) and returns it with the
// plaintext token, which is never stored.
func (s *Store) Create(c CreateOpts) (*Run, string, error) {
	if c.RunID == "" {
		c.RunID = ids.RunID()
	}
	if c.AgentID == "" {
		return nil, "", fmt.Errorf("store: agent_id required")
	}
	if c.Token == "" {
		c.Token = ids.RunToken()
	}
	if c.Plan == nil {
		c.Plan = []proto.PlanStep{}
	}
	s.mu.Lock()
	if _, dup := s.runs[c.RunID]; dup {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("store: run %s exists", c.RunID)
	}
	s.mu.Unlock()
	dir := paths.RunDir(s.dir, c.RunID)
	if err := os.MkdirAll(filepath.Join(dir, paths.InboxDir), 0o700); err != nil {
		return nil, "", err
	}
	rec := proto.RunRecord{
		RunID: c.RunID, AgentID: c.AgentID, Callsign: c.Callsign, AccountID: c.AccountID,
		Role: c.Role, Kind: c.Kind, Model: c.Model, Effort: c.Effort, Task: c.Task, Repo: c.Repo,
		Branch: c.Branch, BaseBranch: c.Base, Worktree: c.Worktree, Actor: c.Actor, Status: proto.StatusStarting,
		CreatedAt: nowTS(time.Now()), TokenSHA256: ids.HashToken(c.Token), Plan: c.Plan,
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, "", err
	}
	if err := WriteFileAtomic(filepath.Join(dir, paths.RunRecordFile), b, 0o644); err != nil {
		return nil, "", err
	}
	r, err := s.loadRun(dir)
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	s.runs[r.id] = r
	s.mu.Unlock()
	return r, c.Token, nil
}

// Remove closes the run, forgets it and deletes its folder (events, inbox, run.json). The caller
// must make sure it is ended; its token stops working. Unknown id: error.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	r := s.runs[id]
	if r == nil {
		s.mu.Unlock()
		return fmt.Errorf("store: no run %s", id)
	}
	delete(s.runs, id)
	s.mu.Unlock()
	_ = r.Close() // Windows cannot delete an open file
	return os.RemoveAll(r.dir)
}
