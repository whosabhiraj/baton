package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/audit"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/redact"
	"github.com/whosabhiraj/baton/internal/store"
)

// Access requests (PROTOCOL.md A20): a sandboxed agent's driver asks the user for something the
// sandbox blocks. Approving one has no effect in the hub.

const (
	accessSummaryMax = 300
	accessWaitMax    = 60 * time.Second
	accessPollEvery  = 200 * time.Millisecond
)

// accessRun authenticates an access route: that run's token or admin. The conductor token is
// refused. A run that ended has a revoked token; it still works here so the driver can read the
// final answer. It writes the error response itself.
func (a *api) accessRun(w http.ResponseWriter, r *http.Request) *store.Run {
	tok := bearer(r)
	if tok == "" {
		writeErr(w, proto.NewError(proto.CodeUnauthorized, "run or admin token required"))
		return nil
	}
	id := r.PathValue("id")
	if a.isAdmin(tok) {
		if a.Touch != nil {
			a.Touch()
		}
		run := a.findRun(id)
		if run == nil {
			writeErr(w, proto.NewError(proto.CodeNotFound, "no such run"))
		}
		return run
	}
	if a.isConductor(tok) {
		writeErr(w, proto.NewError(proto.CodeForbidden, "the conductor cannot use access requests"))
		return nil
	}
	owner := a.Store.RunByToken(tok)
	if owner == nil || (owner.TokenRevoked() && !owner.Ended()) {
		writeErr(w, proto.NewError(proto.CodeUnauthorized, "valid run token required"))
		return nil
	}
	if owner.ID() != id {
		writeErr(w, proto.NewError(proto.CodeRunMismatch, "token belongs to another run"))
		return nil
	}
	return owner
}

func capRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (a *api) accessCreate(w http.ResponseWriter, r *http.Request) {
	run := a.accessRun(w, r)
	if run == nil {
		return
	}
	if a.Approvals == nil {
		writeErr(w, proto.NewError(proto.CodeNotSupported, "approvals are not available"))
		return
	}
	body, perr := readObject(r)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	kind, _ := body["kind"].(string)
	switch kind {
	case "command", "file_change", "permissions":
	default:
		writeErr(w, proto.NewError(proto.CodeInvalidPayload, "kind must be command, file_change or permissions"))
		return
	}
	summary, _ := body["summary"].(string)
	summary = capRunes(redact.String(summary), accessSummaryMax)
	if summary == "" {
		writeErr(w, proto.NewError(proto.CodeInvalidPayload, "summary required"))
		return
	}
	if run.Ended() {
		writeErr(w, proto.NewError(proto.CodeRunEnded, "run has ended"))
		return
	}
	detail := map[string]any{}
	if d, ok := body["detail"]; ok && d != nil {
		dm, ok := d.(map[string]any)
		if !ok {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "detail must be an object"))
			return
		}
		if rd, ok := redact.Value(dm).(map[string]any); ok {
			detail = rd
		}
	}
	detail["kind"], detail["summary"], detail["agent_id"] = kind, summary, run.AgentID()
	who := run.Record().Callsign
	if who == "" {
		who = run.AgentID()
	}
	ap, err := a.Approvals.Add(approvals.KindAccess, run.ID(), who+": "+summary, detail, audit.ActorAgent)
	if err != nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "cannot save approval"))
		return
	}
	a.record(audit.ActorAgent, "approval.request", ap.ID, map[string]any{"kind": ap.Kind, "run_id": ap.RunID})
	if a.Bus != nil {
		a.Bus.PublishNotice("approval", map[string]any{"approval": ap})
	}
	if run.Ended() { // the run ended while we stored it
		a.rejectAccess(ap.ID, "run ended")
		ap, _ = a.Approvals.Get(ap.ID)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "approval": ap})
}

// accessApproval resolves {aid} for the run: only an access approval of that run is visible.
func (a *api) accessApproval(w http.ResponseWriter, r *http.Request, run *store.Run) (approvals.Approval, bool) {
	var ap approvals.Approval
	ok := false
	if a.Approvals != nil {
		ap, ok = a.Approvals.Get(r.PathValue("aid"))
	}
	if !ok || ap.Kind != approvals.KindAccess || ap.RunID != run.ID() {
		writeErr(w, proto.NewError(proto.CodeNotFound, "no such access request"))
		return ap, false
	}
	return ap, true
}

func (a *api) accessPoll(w http.ResponseWriter, r *http.Request) {
	run := a.accessRun(w, r)
	if run == nil {
		return
	}
	ap, ok := a.accessApproval(w, r, run)
	if !ok {
		return
	}
	var wait time.Duration
	if v := r.URL.Query().Get("wait"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 0 {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad wait"))
			return
		}
		wait = min(time.Duration(n*float64(time.Second)), accessWaitMax)
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(accessPollEvery)
	defer tick.Stop()
	for ap.Status == approvals.Pending {
		select {
		case <-r.Context().Done():
			return
		case <-a.Done:
			ap, _ = a.Approvals.Get(ap.ID)
			writeJSON(w, 200, map[string]any{"ok": true, "status": ap.Status, "note": ap.Note})
			return
		case <-deadline.C:
			writeJSON(w, 200, map[string]any{"ok": true, "status": ap.Status, "note": ap.Note})
			return
		case <-tick.C:
			ap, _ = a.Approvals.Get(ap.ID)
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "status": ap.Status, "note": ap.Note})
}

func (a *api) accessCancel(w http.ResponseWriter, r *http.Request) {
	run := a.accessRun(w, r)
	if run == nil {
		return
	}
	ap, ok := a.accessApproval(w, r, run)
	if !ok {
		return
	}
	if ap.Status == approvals.Pending {
		a.rejectAccess(ap.ID, "expired")
		ap, _ = a.Approvals.Get(ap.ID)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "status": ap.Status})
}

// rejectAccess rejects a pending access approval on the hub's or the agent's behalf.
func (a *api) rejectAccess(id, note string) {
	by := audit.ActorHub
	if note == "expired" {
		by = audit.ActorAgent
	}
	ap, err := a.Approvals.Decide(id, false, by, note)
	if err != nil {
		if !errors.Is(err, approvals.ErrAlreadyDecided) && a.Logger != nil {
			a.Logger.Warn("access reject failed", "approval", id, "err", err)
		}
		return
	}
	a.record(by, "approval.reject", ap.ID, map[string]any{"kind": ap.Kind, "run_id": ap.RunID, "note": note})
	if a.Bus != nil {
		a.Bus.PublishNotice("approval", map[string]any{"approval": ap})
	}
}

// sweepAccess rejects pending access approvals of runs that ended (runID "" = all runs).
func (a *api) sweepAccess(runID string) {
	for _, ap := range a.Approvals.List(approvals.Pending) {
		if ap.Kind != approvals.KindAccess || (runID != "" && ap.RunID != runID) {
			continue
		}
		if run := a.Store.Get(ap.RunID); run == nil || run.Ended() {
			a.rejectAccess(ap.ID, "run ended")
		}
	}
}

// watchRunEnds rejects the pending access requests of a run when it ends (done, exit, killed).
func (a *api) watchRunEnds() {
	if a.Approvals == nil || a.Bus == nil {
		return
	}
	sub, _, _ := a.Bus.SubscribeInternal(a.Bus.LastID(), true)
	a.sweepAccess("")
	go func() {
		for {
			select {
			case it := <-sub.C:
				if it.Notice == nil {
					switch it.Ev.Type {
					case "done", "exit", "killed":
						a.sweepAccess(it.Ev.RunID)
					}
				}
			case <-sub.Dropped: // fell behind: take a new subscription and sweep everything
				sub.Close()
				sub, _, _ = a.Bus.SubscribeInternal(a.Bus.LastID(), true)
				a.sweepAccess("")
			case <-a.Bus.Closed:
				return
			case <-a.Done:
				sub.Close()
				return
			}
		}
	}()
}
