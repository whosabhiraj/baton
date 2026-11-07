package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
)

// usageKeys are the per-account usage totals (A15).
var usageKeys = []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd"}

func f64(v any) float64 {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

// accountUsage sums the latest cumulative usage of every run of the account.
func (a *api) accountUsage() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, r := range a.Store.List() {
		sum := r.Summary()
		if sum.Usage == nil {
			continue
		}
		t := out[sum.AccountID]
		if t == nil {
			t = map[string]any{}
			out[sum.AccountID] = t
		}
		for _, k := range usageKeys {
			t[k] = f64(t[k]) + f64(sum.Usage[k])
		}
	}
	if a.ConductorCtl != nil { // the conductor is not a run, but it spends its account's quota too
		for acct, u := range a.ConductorCtl.Usage() {
			t := out[acct]
			if t == nil {
				t = map[string]any{}
				out[acct] = t
			}
			for _, k := range usageKeys {
				t[k] = f64(t[k]) + f64(u[k])
			}
		}
	}
	return out
}

// usageOf returns the totals with zeros filled in; token counts are integers.
func usageOf(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range usageKeys {
		if k == "cost_usd" {
			out[k] = f64(m[k])
		} else {
			out[k] = int64(f64(m[k]))
		}
	}
	return out
}

func (a *api) conductor() proto.ConductorState {
	if a.Conductor == nil {
		return proto.ConductorState{State: "off"}
	}
	return a.Conductor()
}

func (a *api) pendingApprovals() []approvals.Approval {
	if a.Approvals == nil {
		return []approvals.Approval{}
	}
	return a.Approvals.List(approvals.Pending)
}

func (a *api) listApprovals(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	switch st {
	case "", approvals.Pending, approvals.Approved, approvals.Rejected:
	default:
		writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad status"))
		return
	}
	list := []approvals.Approval{}
	if a.Approvals != nil {
		list = a.Approvals.List(st)
	}
	writeJSON(w, 200, list)
}

func (a *api) decide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		note, _ := body["note"].(string)
		if a.Approvals == nil {
			writeErr(w, proto.NewError(proto.CodeNotFound, "no such approval"))
			return
		}
		// A16: approving a merge performs it first; if the merge fails (conflict, dirty
		// checkout, uncommitted agent work) the approval stays pending and the error is returned.
		if prev, ok := a.Approvals.Get(r.PathValue("id")); ok && approve && prev.Kind == approvals.KindMerge &&
			prev.Status == approvals.Pending && (a.Runs != nil || a.FakeMerges) {
			if run := a.Store.Get(prev.RunID); run != nil && !run.Summary().Merged {
				if _, perr := a.doMerge(r.Context(), actor, run); perr != nil {
					writeErr(w, perr)
					return
				}
			}
		}
		var lineupResult map[string]any
		if prev, ok := a.Approvals.Get(r.PathValue("id")); ok && approve && prev.Kind == approvals.KindLineup && prev.Status == approvals.Pending {
			a.lineupMu.Lock()
			defer a.lineupMu.Unlock()
			if cur, _ := a.Approvals.Get(prev.ID); cur.Status == approvals.Pending { // not approved meanwhile
				agents, perr := a.lineupAgents(prev, body)
				if perr != nil {
					writeErr(w, perr)
					return
				}
				lineupResult = a.spawnLineup(r.Context(), agents)
			}
		}
		ap, err := a.Approvals.Decide(r.PathValue("id"), approve, actor, note)
		switch {
		case errors.Is(err, approvals.ErrNotFound):
			writeErr(w, proto.NewError(proto.CodeNotFound, "no such approval"))
			return
		case errors.Is(err, approvals.ErrAlreadyDecided):
			writeErr(w, proto.NewError(proto.CodeAlreadyDecided, "approval already "+ap.Status))
			return
		case err != nil:
			writeErr(w, proto.NewError(proto.CodeInternal, "cannot save approval"))
			return
		}
		if lineupResult != nil {
			if withRes, err := a.Approvals.SetResult(ap.ID, lineupResult); err == nil {
				ap = withRes
			}
		}
		action := "approval.reject"
		if approve {
			action = "approval.approve"
		}
		a.record(actor, action, ap.ID, map[string]any{"kind": ap.Kind, "run_id": ap.RunID})
		if a.Bus != nil {
			a.Bus.PublishNotice("approval", map[string]any{"approval": ap})
		}
		if a.OnApproval != nil {
			go a.OnApproval(ap)
		}
		writeJSON(w, 200, ap)
	}
}

// kill stops a run through the launcher.
func (a *api) kill(w http.ResponseWriter, r *http.Request) {
	actor, perr := actorOf(r)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	if a.Launcher == nil {
		writeErr(w, proto.NewError(proto.CodeNotSupported, "no launcher: agents cannot be killed here"))
		return
	}
	run := a.findRun(r.PathValue("id"))
	if run == nil {
		writeErr(w, proto.NewError(proto.CodeNotFound, "no such run"))
		return
	}
	if run.Ended() {
		writeErr(w, &proto.Error{Status: http.StatusConflict, Code: proto.CodeRunEnded, Msg: "run has ended"})
		return
	}
	if err := a.Launcher.Kill(run.ID()); err != nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "kill failed: "+err.Error()))
		return
	}
	a.record(actor, "kill", run.ID(), nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// startAgent starts the agent of a fresh spawn. On failure the run is ended with exit code -1.
func (a *api) startAgent(res *runs.SpawnResult) error {
	if a.Launcher == nil || res.DryRun {
		return nil
	}
	err := a.Launcher.Start(*res)
	if err != nil {
		if run := a.Store.Get(res.RunID); run != nil {
			_, _ = run.AppendHub("exit", map[string]any{"code": -1, "signal": nil, "diff_stat": nil, "duration_s": 0, "detail": "launch failed: " + err.Error()})
		}
	}
	return err
}

// launch is startAgent for POST /api/spawn: a failure answers 500 LAUNCH_FAILED (with run_id).
func (a *api) launch(w http.ResponseWriter, res *runs.SpawnResult) bool {
	err := a.startAgent(res)
	if err == nil {
		return true
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "code": proto.CodeLaunchFailed,
		"error": "cannot start agent: " + err.Error(), "run_id": res.RunID})
	return false
}
