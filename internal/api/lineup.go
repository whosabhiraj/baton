package api

// Phase 6d (A18): lineups. The conductor proposes a team (POST /api/lineups); in user/propose
// mode it becomes a `lineup` approval that the user can edit and approve, which spawns the
// agents; in auto mode (and approvals.spawn off) the hub spawns them right away.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/audit"
	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
)

const maxLineup = 20

func (a *api) mode() string {
	if a.Config == nil {
		return config.ModePropose
	}
	return a.Config.Current().Config.Hub.Mode
}

// lineupNeeded: conductor spawns go through a lineup approval (every mode but auto, and auto
// too when approvals.spawn is on).
func (a *api) lineupNeeded() bool {
	if a.Config == nil {
		return true
	}
	h := a.Config.Current().Config.Hub
	return h.Mode != config.ModeAuto || h.Approvals.Spawn
}

// decodeAgents reads a JSON array of spawn requests strictly (unknown fields are errors).
func decodeAgents(raw any) ([]runs.SpawnRequest, *proto.Error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, proto.NewError(proto.CodeInvalidPayload, "agents must be an array")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var agents []runs.SpawnRequest
	if err := dec.Decode(&agents); err != nil {
		return nil, proto.NewError(proto.CodeInvalidPayload, "agents: "+err.Error())
	}
	if len(agents) == 0 || len(agents) > maxLineup {
		return nil, proto.NewError(proto.CodeInvalidPayload, fmt.Sprintf("a lineup has 1 to %d agents", maxLineup))
	}
	for i := range agents {
		agents[i].DryRun, agents[i].Actor = false, ""
	}
	return agents, nil
}

// checkAgents dry-runs every agent. Config errors (account, role, model) are refused now;
// "busy right now" is not (the user may approve later: caps are checked again at spawn time),
// but a lineup that can never fit an account's max_concurrent is.
func (a *api) checkAgents(ctx context.Context, agents []runs.SpawnRequest) *proto.Error {
	if a.Runs == nil {
		return proto.NewError(proto.CodeNotARepo, "hub has no git repository to spawn agents in")
	}
	per := map[string]int{}
	for i, ag := range agents {
		ag.DryRun, ag.Actor = true, audit.ActorConductor
		res, perr := a.Runs.Spawn(ctx, ag)
		if perr != nil && perr.Code != proto.CodeAccountBusy && perr.Code != proto.CodeMaxAgents {
			return &proto.Error{Status: perr.Status, Code: perr.Code, Msg: fmt.Sprintf("agent %d (%s): %s", i+1, ag.Role, perr.Msg)}
		}
		acct := ag.Account
		if res != nil {
			acct = res.AccountID
		}
		per[acct]++
	}
	if a.Config != nil {
		cfg := a.Config.Current().Config
		for id, n := range per {
			if ac, ok := cfg.Account(id); ok && ac.MaxConcurrent > 0 && n > ac.MaxConcurrent {
				return proto.NewError(proto.CodeAccountBusy, fmt.Sprintf("the lineup puts %d agents on %s, which runs at most %d at a time", n, id, ac.MaxConcurrent))
			}
		}
	}
	return nil
}

// spawnLineup spawns the agents one by one (actor conductor: it proposed them) and reports
// what happened; a failure does not stop the others.
func (a *api) spawnLineup(ctx context.Context, agents []runs.SpawnRequest) map[string]any {
	spawned, failed := []map[string]any{}, []map[string]any{}
	for i, ag := range agents {
		ag.Actor = audit.ActorConductor
		if a.Runs == nil {
			failed = append(failed, map[string]any{"index": i, "role": ag.Role, "account": ag.Account, "code": proto.CodeNotARepo, "error": "no git repository"})
			continue
		}
		res, perr := a.Runs.Spawn(context.WithoutCancel(ctx), ag)
		if perr != nil {
			failed = append(failed, map[string]any{"index": i, "role": ag.Role, "account": ag.Account, "code": perr.Code, "error": perr.Msg})
			continue
		}
		a.record(audit.ActorConductor, "spawn", res.RunID, map[string]any{"account": res.AccountID, "role": res.Role, "model": res.Model, "run_id": res.RunID, "lineup": true})
		if err := a.startAgent(res); err != nil {
			failed = append(failed, map[string]any{"index": i, "role": ag.Role, "account": ag.Account, "code": proto.CodeLaunchFailed, "error": err.Error(), "run_id": res.RunID})
			continue
		}
		spawned = append(spawned, map[string]any{"index": i, "run_id": res.RunID, "agent_id": res.AgentID, "callsign": res.Callsign,
			"account": res.AccountID, "role": res.Role, "model": res.Model})
	}
	return map[string]any{"spawned": spawned, "failed": failed}
}

// lineupAgents returns the agents to spawn for an approval: the user's edited list from the
// approve body ("agents"), or the proposed one.
func (a *api) lineupAgents(ap approvals.Approval, body map[string]any) ([]runs.SpawnRequest, *proto.Error) {
	raw, edited := body["agents"]
	if !edited {
		raw = ap.Detail["agents"]
	}
	agents, perr := decodeAgents(raw)
	if perr != nil {
		return nil, perr
	}
	return agents, nil
}

func lineupTitle(goal string, n int) string {
	goal = strings.TrimSpace(goal)
	if i := strings.IndexByte(goal, '\n'); i >= 0 {
		goal = goal[:i]
	}
	if len(goal) > 80 {
		goal = goal[:80] + "..."
	}
	t := fmt.Sprintf("Lineup: %d agent", n)
	if n != 1 {
		t += "s"
	}
	if goal != "" {
		t += " for " + goal
	}
	return t
}

// proposeLineup (POST /api/lineups, body { goal, note?, agents[] }).
func (a *api) proposeLineup(w http.ResponseWriter, r *http.Request) {
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
	for k := range body {
		if k != "goal" && k != "note" && k != "agents" {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "unknown field "+k))
			return
		}
	}
	goal, _ := body["goal"].(string)
	note, _ := body["note"].(string)
	agents, perr := decodeAgents(body["agents"])
	if perr != nil {
		writeErr(w, perr)
		return
	}
	if perr := a.checkAgents(r.Context(), agents); perr != nil {
		writeErr(w, perr)
		return
	}
	if actor == audit.ActorHuman || !a.lineupNeeded() {
		a.lineupMu.Lock()
		res := a.spawnLineup(r.Context(), agents)
		a.lineupMu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true, "status": "spawned", "result": res})
		return
	}
	if a.Approvals == nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "no approval store"))
		return
	}
	detail := map[string]any{"goal": goal, "agents": agents, "mode": a.mode()}
	if note != "" {
		detail["note"] = note
	}
	ap, err := a.Approvals.Add(approvals.KindLineup, "", lineupTitle(goal, len(agents)), detail, actor)
	if err != nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "cannot save approval"))
		return
	}
	a.record(actor, "approval.request", ap.ID, map[string]any{"kind": ap.Kind, "agents": len(agents)})
	if a.Bus != nil {
		a.Bus.PublishNotice("approval", map[string]any{"approval": ap})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "pending", "approval": ap})
}
