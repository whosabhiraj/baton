package api

// Phase 6 (A16): the routes behind the conductor's MCP tools — run list/detail, diff, merge,
// the wait long-poll — and the conductor token's limits.

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/audit"
	"github.com/whosabhiraj/baton/internal/bus"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/store"
)

// Conductor token limits: the only config fields it may change.
var (
	conductorAccountFields = []string{"enabled"}
	conductorHubFields     = []string{"mode"}
)

// WaitTypes are what GET /api/wait returns by default: run events that need the conductor, and
// the hub notices `approval` (decided ones only) and `merged`.
var WaitTypes = []string{"question", "step_blocked", "done", "error", "stalled", "rate_limited", "exit", "approval", "merged"}

const (
	waitDefault  = 60 * time.Second
	waitMax      = 600 * time.Second
	waitLinger   = 250 * time.Millisecond // collect a burst after the first match
	waitMaxItems = 50
)

// findRun resolves a run id, or an agent id (its newest run).
func (a *api) findRun(id string) *store.Run {
	if r := a.Store.Get(id); r != nil {
		return r
	}
	var found *store.Run
	for _, r := range a.Store.List() { // oldest first: the last match is the newest
		if r.AgentID() == id {
			found = r
		}
	}
	return found
}

func (a *api) runOr404(w http.ResponseWriter, r *http.Request) *store.Run {
	run := a.findRun(r.PathValue("id"))
	if run == nil {
		writeErr(w, proto.NewError(proto.CodeNotFound, "no such run or agent"))
	}
	return run
}

// conductorFieldsOK refuses (403 FORBIDDEN) a conductor PATCH that touches other fields.
func conductorFieldsOK(w http.ResponseWriter, r *http.Request, body map[string]any, allowed []string) bool {
	if !isConductorReq(r) {
		return true
	}
	for k := range body {
		if !slices.Contains(allowed, k) {
			writeErr(w, proto.NewError(proto.CodeForbidden, "the conductor may only change: "+strings.Join(allowed, ", ")))
			return false
		}
	}
	return true
}

func splitList(v string) []string {
	out := []string{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---- runs ----

func (a *api) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	statuses := splitList(q.Get("status"))
	active := q.Get("active") == "true" || q.Get("active") == "1"
	out := []store.Summary{}
	for _, run := range a.Store.List() {
		if active && run.Ended() {
			continue
		}
		sum := run.Summary()
		if len(statuses) > 0 && !slices.Contains(statuses, string(sum.Status)) {
			continue
		}
		out = append(out, sum)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "runs": out})
}

// quiet event types are left out of get_agent's recent events (they flood the conductor's context).
var quietTypes = []string{"activity", "heartbeat", "usage", "to_agent"}

func (a *api) getRun(w http.ResponseWriter, r *http.Request) {
	run := a.runOr404(w, r)
	if run == nil {
		return
	}
	n := 10
	if v := r.URL.Query().Get("events"); v != "" {
		x, err := strconv.Atoi(v)
		if err != nil || x < 0 || x > 500 {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "events must be 0..500"))
			return
		}
		n = x
	}
	evs, err := run.Events(0)
	if err != nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "cannot read events"))
		return
	}
	recent, artifacts := []proto.StoredEvent{}, []map[string]any{}
	for _, ev := range evs {
		if ev.Type == "artifact" {
			artifacts = append(artifacts, ev.Payload)
		}
		if !slices.Contains(quietTypes, ev.Type) {
			recent = append(recent, ev)
		}
	}
	if len(recent) > n {
		recent = recent[len(recent)-n:]
	}
	rec := run.Record()
	diffStat := ""
	if a.Runs != nil && rec.Worktree != "" {
		if s, err := a.Runs.DiffStat(r.Context(), run.ID()); err == nil {
			diffStat = s
		}
	}
	pending := []approvals.Approval{}
	for _, ap := range a.pendingApprovals() {
		if ap.RunID == run.ID() {
			pending = append(pending, ap)
		}
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "run": run.Summary(), "task": rec.Task, "effort": rec.Effort, "worktree": rec.Worktree,
		"actor": rec.Actor, "recent_events": recent, "artifacts": artifacts, "diff_stat": diffStat, "approvals": pending,
	})
}

func (a *api) diff(w http.ResponseWriter, r *http.Request) {
	run := a.runOr404(w, r)
	if run == nil {
		return
	}
	if a.Runs == nil {
		writeErr(w, proto.NewError(proto.CodeNotARepo, "hub has no git repository"))
		return
	}
	max := 0
	if v := r.URL.Query().Get("max_bytes"); v != "" {
		x, err := strconv.Atoi(v)
		if err != nil || x < 0 {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad max_bytes"))
			return
		}
		max = x
	}
	res, perr := a.Runs.Diff(r.Context(), run.ID(), max)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "diff": res})
}

// ---- merge ----

// merge (POST /api/runs/:id/merge, body {"confirm": true}). A conductor request opens (or
// returns the already pending) merge approval when approvals.merge is on; a human request, or
// any request with the gate off, merges now.
func (a *api) merge(w http.ResponseWriter, r *http.Request) {
	actor, perr := actorOf(r)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	body, perr := readObject(r)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	if c, _ := body["confirm"].(bool); !c {
		writeErr(w, proto.NewError(proto.CodeConfirmRequired, `merging needs "confirm": true`))
		return
	}
	run := a.runOr404(w, r)
	if run == nil {
		return
	}
	if a.Runs == nil && !a.FakeMerges {
		writeErr(w, proto.NewError(proto.CodeNotARepo, "hub has no git repository"))
		return
	}
	sum := run.Summary()
	if sum.Merged {
		writeJSON(w, 200, map[string]any{"ok": true, "status": "merged", "already_merged": true})
		return
	}
	if !run.Ended() && sum.Status != proto.StatusDone {
		writeErr(w, proto.NewError(proto.CodeRunActive, "agent is still working (status "+string(sum.Status)+"); merge after it reports done"))
		return
	}
	gate := true
	if a.Config != nil {
		gate = a.Config.Current().Config.Hub.Approvals.Merge
	}
	if actor == audit.ActorConductor && gate && a.Approvals != nil {
		if ap, ok := a.Approvals.Pending(approvals.KindMerge, run.ID()); ok {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "pending", "approval": ap})
			return
		}
		note, _ := body["note"].(string)
		title := "Merge " + sum.Callsign + " (" + sum.Branch + ") into " + sum.BaseBranch
		detail := map[string]any{"branch": sum.Branch, "base_branch": sum.BaseBranch, "agent_id": sum.AgentID}
		if note != "" {
			detail["note"] = note
		}
		if a.Runs != nil {
			if s, err := a.Runs.DiffStat(r.Context(), run.ID()); err == nil {
				detail["diff_stat"] = s
			}
			if h, perr := a.Runs.BranchHead(r.Context(), run.ID()); perr == nil {
				detail["head"] = h // approving merges exactly this commit
			}
		}
		ap, err := a.Approvals.Add(approvals.KindMerge, run.ID(), title, detail, actor)
		if err != nil {
			writeErr(w, proto.NewError(proto.CodeInternal, "cannot save approval"))
			return
		}
		a.record(actor, "approval.request", ap.ID, map[string]any{"kind": ap.Kind, "run_id": ap.RunID})
		if a.Bus != nil {
			a.Bus.PublishNotice("approval", map[string]any{"approval": ap})
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "pending", "approval": ap})
		return
	}
	res, perr := a.doMerge(r.Context(), actor, run)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "status": "merged", "merge": res})
}

// doMerge merges the run's branch, marks the run merged, audits it and publishes `merged`.
func (a *api) doMerge(ctx context.Context, actor string, run *store.Run) (map[string]any, *proto.Error) {
	out := map[string]any{"run_id": run.ID()}
	if !a.FakeMerges {
		if a.Runs == nil {
			return nil, proto.NewError(proto.CodeNotARepo, "hub has no git repository")
		}
		res, perr := a.Runs.Merge(context.WithoutCancel(ctx), run.ID())
		if perr != nil {
			return nil, perr
		}
		out = map[string]any{"run_id": res.RunID, "branch": res.Branch, "base_branch": res.BaseBranch,
			"commit": res.Commit, "already_merged": res.AlreadyMerged, "method": res.Method}
		_ = run.SetExtra("merge_commit", res.Commit)
	}
	if err := run.SetExtra("merged", true); err != nil {
		return nil, proto.NewError(proto.CodeInternal, "merged, but cannot record it: "+err.Error())
	}
	a.record(actor, "merge", run.ID(), out)
	if a.Bus != nil {
		n := map[string]any{"run_id": run.ID(), "agent_id": run.AgentID()}
		if c, ok := out["commit"]; ok {
			n["commit"] = c
		}
		a.Bus.PublishNotice("merged", n)
	}
	return out, nil
}

// ---- wait ----

type waitItem struct {
	N        uint64         `json:"n"`
	Kind     string         `json:"kind"` // event | notice
	Type     string         `json:"type"`
	RunID    string         `json:"run_id,omitempty"`
	AgentID  string         `json:"agent_id,omitempty"`
	Callsign string         `json:"callsign,omitempty"`
	Seq      int64          `json:"seq,omitempty"`
	TS       string         `json:"ts,omitempty"`
	Payload  map[string]any `json:"payload,omitempty"`
}

func (a *api) waitMatch(it bus.Item, types []string) (waitItem, bool) {
	if it.Notice != nil {
		t := it.Notice.Type
		if !slices.Contains(types, t) {
			return waitItem{}, false
		}
		if ap, ok := it.Notice.Data["approval"].(approvals.Approval); ok && t == "approval" && ap.Status == approvals.Pending {
			return waitItem{}, false // the request itself is not news to the conductor
		}
		return waitItem{N: it.N, Kind: "notice", Type: t, Payload: it.Notice.Data}, true
	}
	if !slices.Contains(types, it.Ev.Type) {
		return waitItem{}, false
	}
	wi := waitItem{N: it.N, Kind: "event", Type: it.Ev.Type, RunID: it.Ev.RunID, AgentID: it.Ev.AgentID,
		Seq: it.Ev.Seq, TS: it.Ev.TS, Payload: it.Ev.Payload}
	if run := a.Store.Get(it.Ev.RunID); run != nil {
		wi.Callsign = run.Record().Callsign
	}
	return wi, true
}

// wait (GET /api/wait?cursor=&instance=&timeout_s=&types=) blocks until at least one matching item
// arrives after cursor (absent = now), then returns a short burst and the new cursor. reset:
// the cursor is unknown to this hub (restart, or too old): re-read the run list.
func (a *api) wait(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	timeout := waitDefault
	if v := q.Get("timeout_s"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad timeout_s"))
			return
		}
		timeout = min(time.Duration(f*float64(time.Second)), waitMax)
	}
	types := WaitTypes
	if v := q.Get("types"); v != "" {
		types = splitList(v)
	}
	cursor := a.Bus.LastID()
	cond := isConductorReq(r) && a.ConductorCtl != nil
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad cursor"))
			return
		}
		cursor = n
	} else if cond {
		// A fresh `baton mcp` (conductor restart) starts where the conductor last read, not at
		// "now": the event that woke it must still be returned.
		cursor = min(cursor, a.ConductorCtl.SeenCursor())
	}
	if q.Get("types") == "-" { // pin only: report the start cursor, consume nothing
		writeJSON(w, 200, map[string]any{"ok": true, "cursor": cursor, "instance": a.Instance, "items": []waitItem{},
			"timed_out": false, "reset": false})
		return
	}
	sub, replay, reset := a.Bus.Subscribe(cursor, true)
	defer sub.Close()
	if inst := q.Get("instance"); inst != "" && inst != a.Instance {
		reset = true // the cursor belongs to an earlier hub process
	}
	if reset {
		writeJSON(w, 200, map[string]any{"ok": true, "cursor": a.Bus.LastID(), "instance": a.Instance, "items": []waitItem{},
			"timed_out": false, "reset": true})
		return
	}
	items := []waitItem{}
	take := func(it bus.Item) bool { // false = batch full
		if len(items) >= waitMaxItems {
			return false
		}
		cursor = it.N
		if wi, ok := a.waitMatch(it, types); ok {
			items = append(items, wi)
		}
		return true
	}
	full := false
	for _, it := range replay {
		if full = !take(it); full {
			break
		}
	}
	timedOut, dropped := false, false
	if len(items) == 0 && !full {
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
	wait:
		for {
			select {
			case it := <-sub.C:
				take(it)
				if len(items) > 0 {
					break wait
				}
			case <-deadline.C:
				timedOut = true
				break wait
			case <-sub.Dropped:
				dropped = true
				break wait
			case <-r.Context().Done():
				return
			case <-a.Done:
				break wait
			case <-a.Bus.Closed:
				break wait
			}
		}
	}
	if len(items) > 0 && !full && !dropped {
		linger := time.NewTimer(waitLinger)
		defer linger.Stop()
	burst:
		for {
			select {
			case it := <-sub.C:
				if !take(it) {
					break burst
				}
			case <-linger.C:
				break burst
			case <-r.Context().Done():
				return
			}
		}
	}
	if cond && !dropped && q.Get("types") == "" { // a filtered wait skips items it did not show
		a.ConductorCtl.Seen(cursor)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "cursor": cursor, "instance": a.Instance, "items": items,
		"timed_out": timedOut, "reset": dropped})
}
