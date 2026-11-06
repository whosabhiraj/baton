package hub_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/proto"
)

func gitEnv(t *testing.T) {
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "t")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t@t")
	}
}

// repoHub starts a hub on a fresh git repo with a fake launcher.
func repoHub(t *testing.T) (*hub.Hub, string) {
	t.Helper()
	gitEnv(t)
	o, _ := p2opts(t)
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

func TestConductorTokenLimits(t *testing.T) {
	h, _ := repoHub(t)
	if st, _ := call(t, h, "GET", "/api/accounts", "bt_cond_nope", ""); st != 401 {
		t.Fatal("unminted conductor token accepted", st)
	}
	tok := h.MintConductorToken()
	for _, p := range []string{"/api/accounts", "/api/state", "/api/runs", "/api/approvals", "/api/config", "/api/audit"} {
		if st, r := call(t, h, "GET", p, tok, ""); st != 200 {
			t.Fatal(p, st, r)
		}
	}
	// admin-only routes stay closed
	for _, c := range [][2]string{{"POST", "/api/approvals/x/approve"}, {"POST", "/api/approvals/x/reject"},
		{"POST", "/api/hub/shutdown"}, {"POST", "/api/dev/runs"}, {"GET", "/api/stream"}} {
		if st, _ := call(t, h, c[0], c[1], tok, ""); st != 401 {
			t.Fatal(c, st)
		}
	}
	// config: only hub.mode and account.enabled
	if st, r := call(t, h, "PATCH", "/api/config", tok, `{"mode":"propose"}`); st != 200 {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "PATCH", "/api/config", tok, `{"idle_stop_min":5}`); st != 403 || r["code"] != "FORBIDDEN" {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "PATCH", "/api/accounts/a1", tok, `{"enabled":true}`); st != 200 {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "PATCH", "/api/accounts/a1", tok, `{"enabled":true,"max_concurrent":3}`); st != 403 {
		t.Fatal(st, r)
	}
	// the actor is forced to conductor, whatever the body or header says
	st, r := call(t, h, "POST", "/api/spawn", tok, `{"account":"a1","role":"implementer","task":"t","actor":"human"}`)
	if st != 200 {
		t.Fatal(st, r)
	}
	run := h.Store().Get(r["run_id"].(string))
	if run.Record().Actor != "conductor" {
		t.Fatal(run.Record().Actor)
	}
	if st, r := call(t, h, "POST", "/api/runs/"+run.AgentID()+"/message", tok, `{"text":"hi","from":"human"}`); st != 200 {
		t.Fatal(st, r)
	}
	msgs, _ := run.Inbox(0)
	if len(msgs) != 1 || msgs[0].Payload["from"] != "conductor" {
		t.Fatal(msgs)
	}
	_, a := call(t, h, "GET", "/api/audit?limit=0", "tok", "")
	for _, e := range a["entries"].([]any) {
		if em := e.(map[string]any); em["action"] == "spawn" && em["actor"] != "conductor" {
			t.Fatal(em)
		}
	}
	// a new session invalidates the old token
	tok2 := h.MintConductorToken()
	if st, _ := call(t, h, "GET", "/api/accounts", tok, ""); st != 401 {
		t.Fatal("old conductor token still valid")
	}
	h.RevokeConductorToken()
	if st, _ := call(t, h, "GET", "/api/accounts", tok2, ""); st != 401 {
		t.Fatal("revoked conductor token still valid")
	}
}

func TestRunsListDetailAndEventFilters(t *testing.T) {
	o, _ := p2opts(t)
	o.Dev = true
	h := start(t, o)
	run := devRun(t, h, `{"role":"x","plan":[{"id":"s1","title":"a","acceptance":"b"}]}`)
	rid, aid := run["run_id"].(string), run["agent_id"].(string)
	for i, typ := range []string{"hello", "progress", "heartbeat", "progress"} {
		pl := map[string]string{"hello": `{"brief_ack":true}`, "heartbeat": "{}"}[typ]
		if typ == "progress" {
			pl = fmt.Sprintf(`{"percent":%d,"note":"n%d"}`, i*10, i)
		}
		if st, r := ingest(t, h, run, typ, fmt.Sprintf("m%d", i), pl); st != 200 {
			t.Fatal(typ, st, r)
		}
	}
	_, l := call(t, h, "GET", "/api/runs?active=true", "tok", "")
	if len(l["runs"].([]any)) != 1 {
		t.Fatal(l)
	}
	_, l = call(t, h, "GET", "/api/runs?status=done", "tok", "")
	if len(l["runs"].([]any)) != 0 {
		t.Fatal(l)
	}
	st, d := call(t, h, "GET", "/api/runs/"+aid+"?events=3", "tok", "") // agent id resolves to its run
	if st != 200 || d["run"].(map[string]any)["run_id"] != rid {
		t.Fatal(st, d)
	}
	recent := d["recent_events"].([]any)
	if len(recent) != 3 || recent[0].(map[string]any)["type"] != "hello" || recent[1].(map[string]any)["type"] != "progress" {
		t.Fatal("heartbeat should be filtered out of recent events:", recent)
	}
	_, e := call(t, h, "GET", "/api/runs/"+rid+"/events?types=progress&limit=1", "tok", "")
	evs := e["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["payload"].(map[string]any)["note"] != "n3" {
		t.Fatal(evs)
	}
	if st, _ := call(t, h, "GET", "/api/runs/nope", "tok", ""); st != 404 {
		t.Fatal(st)
	}
}

func TestWaitLongPoll(t *testing.T) {
	o, _ := p2opts(t)
	o.Dev = true
	h := start(t, o)
	tok := h.MintConductorToken()
	run := devRun(t, h, `{"role":"x"}`)

	_, w := call(t, h, "GET", "/api/wait?timeout_s=0", tok, "")
	if w["timed_out"] != true || len(w["items"].([]any)) != 0 || w["instance"] == "" {
		t.Fatal(w)
	}
	cur, inst := w["cursor"].(interface{ String() string }).String(), w["instance"].(string)

	for _, e := range [][3]string{{"progress", "p1", `{"percent":5,"note":"x"}`}, // not a wake-up type
		{"question", "q1", `{"qid":"q_1","text":"which db?","blocking":false}`}} {
		if st, r := ingest(t, h, run, e[0], e[1], e[2]); st != 200 {
			t.Fatal(st, r)
		}
	}
	_, w = call(t, h, "GET", "/api/wait?timeout_s=5&cursor="+cur+"&instance="+inst, tok, "")
	items := w["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["type"] != "question" || items[0].(map[string]any)["kind"] != "event" {
		t.Fatal(w)
	}
	cur = w["cursor"].(interface{ String() string }).String()

	// nothing new: times out
	_, w = call(t, h, "GET", "/api/wait?timeout_s=0.2&cursor="+cur, tok, "")
	if w["timed_out"] != true || len(w["items"].([]any)) != 0 {
		t.Fatal(w)
	}
	cur = w["cursor"].(interface{ String() string }).String()

	// blocks until done arrives
	go func() {
		time.Sleep(150 * time.Millisecond)
		ingest(t, h, run, "done", "d1", `{"status":"success","summary":"ok"}`)
	}()
	t0 := time.Now()
	_, w = call(t, h, "GET", "/api/wait?timeout_s=5&cursor="+cur, tok, "")
	if time.Since(t0) < 100*time.Millisecond || len(w["items"].([]any)) != 1 || w["items"].([]any)[0].(map[string]any)["type"] != "done" {
		t.Fatal(w)
	}

	// another hub instance's cursor, or one from the future: reset
	if _, w = call(t, h, "GET", "/api/wait?timeout_s=0&cursor=1&instance=other", tok, ""); w["reset"] != true {
		t.Fatal(w)
	}
	if _, w = call(t, h, "GET", "/api/wait?timeout_s=0&cursor=999999", tok, ""); w["reset"] != true {
		t.Fatal(w)
	}
	if st, _ := call(t, h, "GET", "/api/wait?timeout_s=x", tok, ""); st != 400 {
		t.Fatal(st)
	}
}

func spawnAndCommit(t *testing.T, h *hub.Hub, tok, file string) map[string]any {
	t.Helper()
	st, r := call(t, h, "POST", "/api/spawn", tok, `{"account":"a1","role":"implementer","task":"add `+file+`"}`)
	if st != 200 {
		t.Fatal(st, r)
	}
	wt := r["worktree"].(string)
	if err := os.WriteFile(filepath.Join(wt, file), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := gitx.Run(ctx, wt, "add", file); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(ctx, wt, "commit", "-m", "add "+file); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMergeThroughApproval(t *testing.T) {
	h, repo := repoHub(t)
	tok := h.MintConductorToken()
	_, w := call(t, h, "GET", "/api/wait?timeout_s=0", tok, "")
	cur := w["cursor"].(interface{ String() string }).String()

	r := spawnAndCommit(t, h, tok, "feature.txt")
	rid := r["run_id"].(string)
	run := map[string]any{"run_id": rid, "agent_id": r["agent_id"], "token": r["env"].(map[string]any)["BATON_TOKEN"]}

	st, d := call(t, h, "GET", "/api/runs/"+rid+"/diff", tok, "")
	if st != 200 || !strings.Contains(d["diff"].(map[string]any)["diff"].(string), "feature.txt") {
		t.Fatal(st, d)
	}
	if st, m := call(t, h, "POST", "/api/runs/"+rid+"/merge", tok, `{"confirm":true}`); st != 409 || m["code"] != "RUN_ACTIVE" {
		t.Fatal(st, m)
	}
	ingest(t, h, run, "done", "d1", `{"status":"success","summary":"ok"}`)
	if st, m := call(t, h, "POST", "/api/runs/"+rid+"/merge", tok, `{}`); st != 400 || m["code"] != "CONFIRM_REQUIRED" {
		t.Fatal(st, m)
	}
	st, m := call(t, h, "POST", "/api/runs/"+rid+"/merge", tok, `{"confirm":true,"note":"tests pass"}`)
	if st != 202 || m["status"] != "pending" {
		t.Fatal(st, m)
	}
	apID := m["approval"].(map[string]any)["id"].(string)
	if _, m2 := call(t, h, "POST", "/api/runs/"+rid+"/merge", tok, `{"confirm":true}`); m2["approval"].(map[string]any)["id"] != apID {
		t.Fatal("second request opened another approval")
	}

	// dirty user checkout: approving fails and the approval stays pending
	readme := filepath.Join(repo, "tracked.txt")
	os.WriteFile(readme, []byte("v1\n"), 0o644)
	gitx.Run(context.Background(), repo, "add", "tracked.txt")
	gitx.Run(context.Background(), repo, "commit", "-m", "tracked")
	os.WriteFile(readme, []byte("v2 (uncommitted)\n"), 0o644)
	if st, m := call(t, h, "POST", "/api/approvals/"+apID+"/approve", "tok", ""); st != 409 || m["code"] != "MERGE_BLOCKED" {
		t.Fatal(st, m)
	}
	if _, s := call(t, h, "GET", "/api/state", "tok", ""); len(s["approvals"].([]any)) != 1 {
		t.Fatal(s["approvals"])
	}
	gitx.Run(context.Background(), repo, "checkout", "--", "tracked.txt")

	if st, m := call(t, h, "POST", "/api/approvals/"+apID+"/approve", "tok", ""); st != 200 || m["status"] != "approved" {
		t.Fatal(st, m)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatal("feature.txt not merged into the checkout")
	}
	_, s := call(t, h, "GET", "/api/state", "tok", "")
	if err := proto.ValidateJSON("api.state", s); err != nil {
		t.Fatal(err)
	}
	if !h.Store().Get(rid).Summary().Merged {
		t.Fatal("run not marked merged")
	}
	if st, m := call(t, h, "POST", "/api/runs/"+rid+"/merge", tok, `{"confirm":true}`); st != 200 || m["already_merged"] != true {
		t.Fatal(st, m)
	}

	// the conductor hears about the decision and the merge, not about its own request
	var types []string
	for len(types) < 4 {
		_, w = call(t, h, "GET", "/api/wait?timeout_s=1&cursor="+cur, tok, "")
		if w["timed_out"] == true {
			break
		}
		cur = w["cursor"].(interface{ String() string }).String()
		for _, it := range w["items"].([]any) {
			types = append(types, it.(map[string]any)["type"].(string))
		}
	}
	if got := strings.Join(types, ","); got != "done,merged,approval" {
		t.Fatal(got)
	}
}

func TestConductorRoutes(t *testing.T) {
	o, _ := p2opts(t)
	h := start(t, o) // no ConductorExe: no conductor
	if st, r := call(t, h, "GET", "/api/conductor", "tok", ""); st != 409 || r["code"] != "NO_CONDUCTOR" {
		t.Fatal(st, r)
	}
	h.Close(context.Background())

	o.Repo = filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(o.Repo, 0o755)
	if err := gitx.Init(o.Repo); err != nil {
		t.Fatal(err)
	}
	o.ConductorExe, o.ConductorCLI = "baton", "baton-test-no-such-cli"
	h = start(t, o)
	st, r := call(t, h, "GET", "/api/conductor", "tok", "")
	c, _ := r["conductor"].(map[string]any)
	if st != 200 || c["running"] != false || c["problem"] == nil { // the test config has no claude account
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "POST", "/api/conductor/start", "tok", ""); st != 409 || r["code"] != "NO_CONDUCTOR" {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "POST", "/api/conductor/send", "tok", `{}`); st != 400 {
		t.Fatal(st, r)
	}
	if st, r := call(t, h, "GET", "/api/conductor/history?after=0", "tok", ""); st != 200 || len(r["entries"].([]any)) != 0 {
		t.Fatal(st, r)
	}
	tok := h.MintConductorToken()
	if st, _ := call(t, h, "POST", "/api/conductor/send", tok, `{"text":"x"}`); st != 401 {
		t.Fatal("the conductor token must not drive the conductor routes")
	}
}
