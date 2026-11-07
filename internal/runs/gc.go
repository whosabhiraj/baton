package runs

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/whosabhiraj/baton/internal/audit"
	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/gitx"
)

// GCOptions controls Manager.GC.
type GCOptions struct {
	DryRun     bool // report only, change nothing
	PolicyOnly bool // worktree policy only: leave run folders alone (the hub's automatic pass)
	Force      bool // also remove worktrees with uncommitted changes
	Actor      string
	Audit      *audit.Log // nil = no audit entries
}

// GCItem is one thing GC removes (or, with Skipped set, would have removed).
type GCItem struct {
	RunID   string `json:"run_id"`
	AgentID string `json:"agent_id"`
	What    string `json:"what"` // worktree | run
	Reason  string `json:"reason"`
	Skipped string `json:"skipped"` // "" = removed (or would be); else why not
}

// GCReport is the result of Manager.GC.
type GCReport struct {
	Items []GCItem `json:"items"`
}

// GC removes worktrees the cleanup policy allows and ended run folders older than
// hub.runs_keep_days. Live runs are never touched and branches are never deleted.
func (m *Manager) GC(ctx context.Context, now time.Time, o GCOptions) GCReport {
	cfg := m.o.Config().Hub
	rep := GCReport{Items: []GCItem{}}
	policy := map[string]bool{}
	for _, id := range m.CleanupDue(now) {
		policy[id] = true
	}
	for _, r := range m.o.Store.List() {
		if !r.Ended() {
			continue
		}
		rec := r.Record()
		oldRun := false
		if cfg.RunsKeepDays > 0 && !o.PolicyOnly {
			if t, err := time.Parse(time.RFC3339Nano, rec.EndedAt); err == nil {
				oldRun = !now.Before(t.Add(time.Duration(cfg.RunsKeepDays) * 24 * time.Hour))
			}
		}
		if !policy[rec.RunID] && !oldRun {
			continue
		}
		wtGone := true // no worktree left to deal with
		if rec.Worktree != "" {
			if _, err := os.Stat(rec.Worktree); err == nil {
				wtGone = false
			}
		}
		blocked := ""
		if !wtGone {
			it := GCItem{RunID: rec.RunID, AgentID: rec.AgentID, What: "worktree", Reason: m.worktreeReason(cfg)}
			if !policy[rec.RunID] {
				it.Reason = fmt.Sprintf("run folder older than %d days", cfg.RunsKeepDays)
			}
			if dirty, err := m.uncommitted(ctx, rec.Worktree); err != nil {
				it.Skipped = "cannot check for uncommitted changes: " + err.Error()
			} else if len(dirty) > 0 && !o.Force {
				it.Skipped = "uncommitted changes"
			}
			if it.Skipped == "" && !o.DryRun {
				if err := m.RemoveWorktree(ctx, rec.RunID, false); err != nil {
					it.Skipped = "error: " + err.Error()
				} else {
					m.gcAudit(o, "gc.worktree", rec.RunID, it.Reason)
				}
			}
			blocked = it.Skipped
			rep.Items = append(rep.Items, it)
		}
		if oldRun {
			it := GCItem{RunID: rec.RunID, AgentID: rec.AgentID, What: "run", Reason: fmt.Sprintf("ended more than %d days ago", cfg.RunsKeepDays)}
			if blocked != "" {
				it.Skipped = "worktree not removed: " + blocked
			} else if !o.DryRun {
				if err := m.o.Store.Remove(rec.RunID); err != nil {
					it.Skipped = "error: " + err.Error()
				} else {
					m.gcAudit(o, "gc.run", rec.RunID, it.Reason)
				}
			}
			rep.Items = append(rep.Items, it)
		}
	}
	if !o.DryRun && len(rep.Items) > 0 {
		if _, err := gitx.Run(ctx, m.o.Repo, "worktree", "prune"); err != nil {
			m.log.Warn("git worktree prune failed", "err", err)
		}
	}
	return rep
}

func (m *Manager) worktreeReason(cfg config.Hub) string {
	if cfg.Worktrees.Cleanup == config.CleanupRemoveOnMerge {
		return "merged"
	}
	d := cfg.Worktrees.Days
	if d <= 0 {
		d = 14
	}
	return fmt.Sprintf("ended more than %d days ago", d)
}

func (m *Manager) gcAudit(o GCOptions, action, runID, reason string) {
	if o.Audit == nil {
		return
	}
	actor := o.Actor
	if actor == "" {
		actor = audit.ActorHuman
	}
	if err := o.Audit.Append(audit.Entry{Actor: actor, Action: action, Target: runID, Detail: map[string]any{"reason": reason}}); err != nil {
		m.log.Warn("audit append failed", "action", action, "err", err)
	}
}
