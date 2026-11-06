// Package approvals keeps the human approval requests (merge, spawn) in
// <dataDir>/approvals.json. The file is rewritten atomically on every change.
package approvals

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/store"
)

// File is the file name inside the data dir.
const File = "approvals.json"

// Kinds and statuses.
const (
	KindMerge = "merge"
	KindSpawn = "spawn"

	Pending  = "pending"
	Approved = "approved"
	Rejected = "rejected"
)

// Errors returned by Decide.
var (
	ErrNotFound       = errors.New("no such approval")
	ErrAlreadyDecided = errors.New("approval already decided")
)

// Approval is one request for a human decision.
type Approval struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"` // merge | spawn
	RunID       string         `json:"run_id,omitempty"`
	Title       string         `json:"title"`
	Detail      map[string]any `json:"detail,omitempty"`
	RequestedBy string         `json:"requested_by"`
	Status      string         `json:"status"`
	CreatedAt   string         `json:"created_at"`
	DecidedAt   string         `json:"decided_at,omitempty"`
	DecidedBy   string         `json:"decided_by,omitempty"`
	Note        string         `json:"note,omitempty"`
}

// Store holds all approvals, oldest first.
type Store struct {
	path string
	mu   sync.Mutex
	list []Approval
}

// Open loads <dataDir>/approvals.json (missing = empty).
func Open(dataDir string) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, File)}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.list); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.list, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(s.path, b, 0o644)
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// Add stores a new pending approval.
func (s *Store) Add(kind, runID, title string, detail map[string]any, by string) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := Approval{ID: "ap_" + strings.TrimPrefix(ids.QID(), "q_"), Kind: kind, RunID: runID, Title: title,
		Detail: detail, RequestedBy: by, Status: Pending, CreatedAt: now()}
	s.list = append(s.list, a)
	if err := s.saveLocked(); err != nil {
		s.list = s.list[:len(s.list)-1]
		return Approval{}, err
	}
	return a, nil
}

// Decide approves or rejects a pending approval.
func (s *Store) Decide(id string, approve bool, by, note string) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		a := &s.list[i]
		if a.ID != id {
			continue
		}
		if a.Status != Pending {
			return *a, ErrAlreadyDecided
		}
		old := *a
		a.Status, a.DecidedAt, a.DecidedBy, a.Note = Rejected, now(), by, note
		if approve {
			a.Status = Approved
		}
		if err := s.saveLocked(); err != nil {
			*a = old
			return Approval{}, err
		}
		return *a, nil
	}
	return Approval{}, ErrNotFound
}

// List returns the approvals oldest first; status "" = all.
func (s *Store) List(status string) []Approval {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Approval{}
	for _, a := range s.list {
		if status == "" || a.Status == status {
			out = append(out, a)
		}
	}
	return out
}
