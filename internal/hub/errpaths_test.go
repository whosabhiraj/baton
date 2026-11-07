package hub_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/hub"
)

const epCfg = `hub:
  mode: auto
  max_agents_auto: 1
accounts:
  - id: a1
    kind: generic
    roles: [implementer]
    max_concurrent: 5
`

// epRepoHub is repoHub with a custom config body.
func epRepoHub(t *testing.T, cfg string) (*hub.Hub, string) {
	t.Helper()
	gitEnv(t)
	o, gp := p2opts(t)
	if err := os.WriteFile(gp, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := gitx.Init(repo); err != nil {
		t.Fatal(err)
	}
	o.Repo, o.Launcher, o.Dev = repo, &fakeLauncher{}, true
	return start(t, o), repo
}

func TestErrPathsSpawnOverHTTP(t *testing.T) {
	h, _ := epRepoHub(t, epCfg)
	ctok := h.MintConductorToken()
	spawn := `{"account":"a1","role":"implementer","task":"t"}`

	// no account, no routing for the role
	if st, r := call(t, h, "POST", "/api/spawn", "tok", `{"role":"implementer","task":"t"}`); st != 400 || r["code"] != "ACCOUNT_REQUIRED" {
		t.Fatal(st, r)
	}
	// the global cap is auto mode + conductor only: the conductor's second agent is refused...
	if st, r := call(t, h, "POST", "/api/spawn", ctok, spawn); st != 200 {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "POST", "/api/spawn", ctok, spawn); st != 409 || r["code"] != "MAX_AGENTS" {
		t.Fatal(st, r)
	}
	// ...a human spawn is not capped globally...
	if st, r := call(t, h, "POST", "/api/spawn", "tok", spawn); st != 200 {
		t.Fatal(st, r)
	}
	// ...and a dry run reports the same refusal without side effects
	if st, r := call(t, h, "POST", "/api/spawn", ctok, `{"account":"a1","role":"implementer","task":"t","dry_run":true}`); st != 409 || r["code"] != "MAX_AGENTS" {
		t.Fatal(st, r)
	}
	// custom callsign already used by a live agent
	if st, r := call(t, h, "POST", "/api/spawn", "tok", `{"account":"a1","role":"implementer","task":"t","callsign":"Zed"}`); st != 200 {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "POST", "/api/spawn", "tok", `{"account":"a1","role":"implementer","task":"t","callsign":"Zed"}`); st != 409 || r["code"] != "CALLSIGN_TAKEN" {
		t.Fatal(st, r)
	}
	// propose mode: the global cap no longer applies, but the conductor must use a lineup
	if st, r := call(t, h, "PATCH", "/api/config", "tok", `{"mode":"propose"}`); st != 200 {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "POST", "/api/spawn", ctok, spawn); st != 409 || r["code"] != "LINEUP_REQUIRED" {
		t.Fatal(st, r)
	}
}

func TestErrPathsConductorForbiddenCodes(t *testing.T) {
	h, _ := epRepoHub(t, epCfg)
	ctok := h.MintConductorToken()
	for _, c := range [][3]string{
		{"PATCH", "/api/config", `{"max_agents_auto":9}`},
		{"PATCH", "/api/config", `{"mode":"auto","approvals":{"merge":"auto"}}`},
		{"PATCH", "/api/accounts/a1", `{"max_concurrent":3}`},
		{"PATCH", "/api/accounts/a1", `{"roles":["x"]}`},
	} {
		st, r := call(t, h, c[0], c[1], ctok, c[2])
		if st != 403 || r["code"] != "FORBIDDEN" {
			t.Errorf("%v: %d %v", c, st, r)
		}
	}
	// nothing was applied by the refused patches
	_, cfg := call(t, h, "GET", "/api/config", ctok, "")
	if s, _ := cfgString(cfg); strings.Contains(s, `"max_agents_auto":9`) {
		t.Fatal("refused patch was applied")
	}
	// admin-only routes: the conductor token is not an admin token
	for _, c := range [][2]string{{"POST", "/api/approvals/x/approve"}, {"POST", "/api/approvals/x/reject"}, {"POST", "/api/hub/shutdown"}} {
		if st, r := call(t, h, c[0], c[1], ctok, ""); st != 401 || r["code"] != "UNAUTHORIZED" {
			t.Errorf("%v: %d %v", c, st, r)
		}
	}
}

func cfgString(m map[string]any) (string, bool) {
	b, err := json.Marshal(m)
	return string(b), err == nil
}

const epMergeCfg = `hub:
  mode: auto
accounts:
  - id: a1
    kind: generic
    roles: [implementer]
    max_concurrent: 1
`

func TestErrPathsMergeConflictAndBlockedOverHTTP(t *testing.T) {
	h, repo := epRepoHub(t, epMergeCfg)
	tok := h.MintConductorToken()
	r := spawnAndCommit(t, h, "tok", "clash.txt") // admin: the conductor never sees run tokens
	rid := r["run_id"].(string)
	run := map[string]any{"run_id": rid, "agent_id": r["agent_id"], "token": r["env"].(map[string]any)["BATON_TOKEN"]}
	ingest(t, h, run, "done", "d1", `{"status":"success","summary":"ok"}`)

	// the human changed the same file on the base branch
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "clash.txt"), []byte("human\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitx.Run(ctx, repo, "add", "clash.txt")
	if _, err := gitx.Run(ctx, repo, "commit", "-m", "human clash"); err != nil {
		t.Fatal(err)
	}
	st, m := call(t, h, "POST", "/api/runs/"+rid+"/merge", tok, `{"confirm":true}`)
	if st != 202 {
		t.Fatal(st, m)
	}
	apID := m["approval"].(map[string]any)["id"].(string)
	if st, m := call(t, h, "POST", "/api/approvals/"+apID+"/approve", "tok", ""); st != 409 || m["code"] != "MERGE_CONFLICT" {
		t.Fatal(st, m)
	}
	// the refusal leaves the approval pending and the checkout clean
	if _, s := call(t, h, "GET", "/api/state", "tok", ""); len(s["approvals"].([]any)) != 1 {
		t.Fatal(s["approvals"])
	}
	if out, _ := gitx.Run(ctx, repo, "status", "--porcelain", "--untracked-files=no"); out != "" {
		t.Fatal("conflict left changes: " + out)
	}
	// rejecting it twice: the second decision is ALREADY_DECIDED
	if st, m := call(t, h, "POST", "/api/approvals/"+apID+"/reject", "tok", ""); st != 200 {
		t.Fatal(st, m)
	}
	if st, m := call(t, h, "POST", "/api/approvals/"+apID+"/approve", "tok", ""); st != 409 || m["code"] != "ALREADY_DECIDED" {
		t.Fatal(st, m)
	}
	// unknown run
	if st, m := call(t, h, "POST", "/api/runs/r_nope/merge", tok, `{"confirm":true}`); st != 404 || m["code"] != "NOT_FOUND" {
		t.Fatal(st, m)
	}
	// unknown approval
	if st, m := call(t, h, "POST", "/api/approvals/ap_nope/approve", "tok", ""); st != 404 || m["code"] != "NOT_FOUND" {
		t.Fatal(st, m)
	}
}
