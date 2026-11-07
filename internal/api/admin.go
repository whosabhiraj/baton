package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/whosabhiraj/baton/internal/audit"
	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
	"github.com/whosabhiraj/baton/internal/store"
)

// ActorHeader names who is calling an admin route: "human" (default) or "conductor".
const ActorHeader = "X-Baton-Actor"

func actorOf(r *http.Request) (string, *proto.Error) {
	if isConductorReq(r) {
		return audit.ActorConductor, nil
	}
	switch v := strings.ToLower(strings.TrimSpace(r.Header.Get(ActorHeader))); v {
	case "":
		return audit.ActorHuman, nil
	case audit.ActorHuman, audit.ActorConductor:
		return v, nil
	default:
		return "", proto.NewError(proto.CodeInvalidPayload, ActorHeader+" must be human or conductor")
	}
}

// record appends an audit entry (best effort: a failing log never fails the request).
func (a *api) record(actor, action, target string, detail map[string]any) {
	if a.Audit == nil {
		return
	}
	if err := a.Audit.Append(audit.Entry{Actor: actor, Action: action, Target: target, Detail: detail}); err != nil && a.Logger != nil {
		a.Logger.Warn("audit append failed", "action", action, "err", err)
	}
}

// recordReq is record with the actor taken from the request header.
func (a *api) recordReq(r *http.Request, action, target string, detail map[string]any) {
	actor, perr := actorOf(r)
	if perr != nil {
		actor = audit.ActorHuman
	}
	a.record(actor, action, target, detail)
}

// ---- accounts ----

type accountView struct {
	ID              string         `json:"id"`
	Label           string         `json:"label"`
	Kind            string         `json:"kind"`
	Color           string         `json:"color"`
	Enabled         bool           `json:"enabled"`
	MaxConcurrent   int            `json:"max_concurrent"`
	Used            int            `json:"used"`
	Roles           []string       `json:"roles"`
	Models          []string       `json:"models"`
	RateLimited     bool           `json:"rate_limited"`
	PermissionsMode string         `json:"permissions_mode"`
	Usage           map[string]any `json:"usage"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return append([]string{}, s...)
}

func (a *api) accounts() []accountView {
	out := []accountView{}
	if a.Config == nil {
		return out
	}
	used, limited := map[string]int{}, map[string]bool{}
	for _, r := range a.Store.List() {
		if r.Ended() {
			continue
		}
		sum := r.Summary()
		used[sum.AccountID]++
		if sum.Status == proto.StatusRateLimited {
			limited[sum.AccountID] = true
		}
	}
	usage := a.accountUsage()
	for _, ac := range a.Config.Current().Config.Accounts {
		mode := ac.Permissions.Mode
		if mode == "" {
			mode = "default"
		}
		out = append(out, accountView{
			ID: ac.ID, Label: ac.Label, Kind: ac.Kind, Color: ac.Color, Enabled: ac.Enabled,
			MaxConcurrent: ac.MaxConcurrent, Used: used[ac.ID], Roles: nonNil(ac.Roles), Models: nonNil(ac.Models),
			RateLimited: limited[ac.ID], PermissionsMode: mode, Usage: usageOf(usage[ac.ID]),
		})
	}
	return out
}

func (a *api) listAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.accounts())
}

// ---- spawn ----

func (a *api) spawn(w http.ResponseWriter, r *http.Request) {
	actor, perr := actorOf(r)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	if a.Runs == nil {
		writeErr(w, proto.NewError(proto.CodeNotARepo, "hub has no git repository to spawn agents in"))
		return
	}
	b, perr := readBody(r)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	var req runs.SpawnRequest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		msg := "body must be a JSON spawn request"
		if err != nil {
			msg += ": " + err.Error()
		}
		writeErr(w, proto.NewError(proto.CodeInvalidPayload, msg))
		return
	}
	if req.Actor == "" || isConductorReq(r) {
		req.Actor = actor
	} else {
		actor = req.Actor
	}
	if isConductorReq(r) && !req.DryRun && a.lineupNeeded() {
		writeErr(w, proto.NewError(proto.CodeLineupRequired,
			"in "+a.mode()+" mode the user approves the team first: use propose_lineup and wait for the approval"))
		return
	}
	res, perr := a.Runs.Spawn(context.WithoutCancel(r.Context()), req)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	target := res.RunID
	if res.DryRun {
		target = ""
	}
	a.record(actor, "spawn", target, map[string]any{
		"account": res.AccountID, "role": res.Role, "model": res.Model, "run_id": res.RunID, "dry_run": res.DryRun,
	})
	if !a.launch(w, res) {
		return
	}
	if isConductorReq(r) {
		// The agent gets its env (with its run token) from the launcher. The conductor never needs
		// it, and whatever the conductor sees ends up in its history and transcript.
		out := *res
		out.Env = nil
		writeJSON(w, 200, &out)
		return
	}
	writeJSON(w, 200, res)
}

// ---- config ----

func (a *api) noConfig(w http.ResponseWriter) bool {
	if a.Config == nil {
		writeErr(w, proto.NewError(proto.CodeNotFound, "config is not available"))
		return true
	}
	return false
}

func issuesOf(is []config.Issue) []config.Issue {
	if is == nil {
		return []config.Issue{}
	}
	return is
}

func (a *api) getConfig(w http.ResponseWriter, r *http.Request) {
	if a.noConfig(w) {
		return
	}
	s := a.Config.Current()
	writeJSON(w, 200, map[string]any{
		"config": s.Config, "issues": issuesOf(s.Issues), "global_path": s.GlobalPath,
		"project_path": s.ProjectPath, "version": s.Version, "ok": s.OK(),
	})
}

// jsonValue turns a decoded JSON value (numbers as json.Number) into what config.Set* accepts.
func jsonValue(v any) (any, bool) {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i), true
		}
		f, err := x.Float64()
		return f, err == nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case bool, string:
		return x, true
	}
	return nil, false
}

func configInvalid(w http.ResponseWriter, err error) {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	issues := []string{}
	for _, l := range lines[1:] {
		issues = append(issues, strings.TrimSpace(l))
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "code": proto.CodeConfigInvalid, "error": lines[0], "issues": issues})
}

func fileBytes(p string) []byte {
	if p == "" {
		return nil
	}
	b, _ := os.ReadFile(p)
	return b
}

// changedFile reports which config file differs from the contents captured before an edit.
func changedFile(s *config.Snapshot, before map[string][]byte) string {
	for _, p := range []string{s.ProjectPath, s.GlobalPath} {
		if p != "" && !bytes.Equal(before[p], fileBytes(p)) {
			return p
		}
	}
	return ""
}

// patchFields applies a PATCH body field by field; condAllowed lists what a conductor-token
// request may change.
func (a *api) patchFields(w http.ResponseWriter, r *http.Request, action, target string, condAllowed []string,
	set func(s *config.Snapshot, field string, v any) error) bool {
	body, perr := readObject(r)
	if perr != nil {
		writeErr(w, perr)
		return false
	}
	if !conductorFieldsOK(w, r, body, condAllowed) {
		return false
	}
	if len(body) == 0 {
		writeErr(w, proto.NewError(proto.CodeInvalidPayload, "no fields to change"))
		return false
	}
	fields := make([]string, 0, len(body))
	vals := map[string]any{}
	for k, raw := range body {
		v, ok := jsonValue(raw)
		if !ok {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "unsupported value for "+k+" (use bool, number, string or list of strings)"))
			return false
		}
		fields = append(fields, k)
		vals[k] = v
	}
	sort.Strings(fields)
	// All or nothing: if a later field is refused, the files go back to how they were.
	a.patchMu.Lock()
	defer a.patchMu.Unlock()
	start := a.Config.Current()
	orig := map[string][]byte{}
	for _, p := range []string{start.GlobalPath, start.ProjectPath} {
		if p != "" {
			orig[p] = fileBytes(p)
		}
	}
	type change struct{ field, file string }
	var done []change
	for _, f := range fields {
		snap := a.Config.Current()
		before := map[string][]byte{snap.GlobalPath: fileBytes(snap.GlobalPath), snap.ProjectPath: fileBytes(snap.ProjectPath)}
		if err := set(snap, f, vals[f]); err != nil {
			restoreFiles(orig)
			a.Config.Reload()
			configInvalid(w, err)
			return false
		}
		done = append(done, change{f, changedFile(snap, before)})
		a.Config.Reload() // later fields must see this write
	}
	for _, c := range done {
		a.recordReq(r, action, target, map[string]any{"field": c.field, "value": vals[c.field], "file": c.file})
	}
	return true
}

// restoreFiles puts config files back to the given contents (nil = the file did not exist).
func restoreFiles(orig map[string][]byte) {
	for p, b := range orig {
		if bytes.Equal(fileBytes(p), b) {
			continue
		}
		if b == nil {
			_ = os.Remove(p)
			continue
		}
		mode := os.FileMode(0o600)
		if fi, err := os.Stat(p); err == nil {
			mode = fi.Mode().Perm()
		}
		_ = store.WriteFileAtomic(p, b, mode)
	}
}

func (a *api) patchAccount(w http.ResponseWriter, r *http.Request) {
	if a.noConfig(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := a.Config.Current().Config.Account(id); !ok {
		writeErr(w, proto.NewError(proto.CodeNotFound, "no such account"))
		return
	}
	if !a.patchFields(w, r, "config.account", id, conductorAccountFields, func(s *config.Snapshot, f string, v any) error {
		return config.SetAccountField(s, id, f, v)
	}) {
		return
	}
	for _, v := range a.accounts() {
		if v.ID == id {
			writeJSON(w, 200, v)
			return
		}
	}
	writeErr(w, proto.NewError(proto.CodeNotFound, "account vanished after the change"))
}

func (a *api) patchConfig(w http.ResponseWriter, r *http.Request) {
	if a.noConfig(w) {
		return
	}
	if !a.patchFields(w, r, "config.hub", "hub", conductorHubFields, func(s *config.Snapshot, f string, v any) error {
		return config.SetHubField(s, f, v)
	}) {
		return
	}
	a.getConfig(w, r)
}

// ---- audit ----

func (a *api) auditList(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad limit"))
			return
		}
		limit = n
	}
	entries := []audit.Entry{}
	if a.Audit != nil {
		es, err := a.Audit.Read(limit)
		if err != nil {
			writeErr(w, proto.NewError(proto.CodeInternal, "cannot read audit log"))
			return
		}
		entries = es
	}
	writeJSON(w, 200, map[string]any{"entries": entries})
}
