package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/proto"
)

const p2Config = `# my accounts
hub:
  mode: propose # keep
accounts:
  - id: a1 # main login
    label: Main
    kind: generic
    roles: [implementer, tester]
    models: [m1]
    max_concurrent: 1
  - id: a2
    kind: generic
    roles: [reviewer]
  - id: off
    kind: generic
    enabled: false
    roles: [implementer]
`

type p2Env struct {
	h      *hub.Hub
	repo   string
	cfg    string
	data   string
	tok    string
	client *http.Client
}

func p2Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t.invalid"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func p2Start(t *testing.T, idle time.Duration) *p2Env {
	t.Helper()
	requireBin(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	p2Git(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p2Git(t, repo, "add", ".")
	p2Git(t, repo, "commit", "-q", "-m", "init")
	cfg := filepath.Join(root, "home", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(p2Config), 0o644); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	if idle == 0 {
		idle = -1
	}
	h, err := hub.Start(context.Background(), hub.Options{
		DataDir: data, Port: 0, AdminToken: "bt_adm_p2", SpoolPoll: 50 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Repo: repo, GlobalConfig: cfg,
		ConfigPoll: 50 * time.Millisecond, IdleStop: idle,
	})
	if err != nil {
		t.Fatalf("hub.Start: %v", err)
	}
	t.Cleanup(func() { closeHub(h) })
	return &p2Env{h: h, repo: repo, cfg: cfg, data: data, tok: h.AdminToken, client: httpClient}
}

// call performs an admin request and returns status and raw body.
func (e *p2Env) call(t *testing.T, method, path string, body any, hdr map[string]string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.h.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.tok)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *p2Env) obj(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	st, b := e.call(t, method, path, body, nil)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s %s: not an object: %s", method, path, b)
	}
	return st, m
}

func (e *p2Env) spawn(t *testing.T, req map[string]any) (int, map[string]any) {
	t.Helper()
	if _, ok := req["task"]; !ok {
		req["task"] = "do the thing"
	}
	return e.obj(t, "POST", "/api/spawn", req)
}

func (e *p2Env) mustSpawn(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	st, m := e.spawn(t, req)
	if st != 200 {
		t.Fatalf("spawn %v: %d %v", req, st, m)
	}
	return m
}

func (e *p2Env) account(t *testing.T, id string) map[string]any {
	t.Helper()
	st, b := e.call(t, "GET", "/api/accounts", nil, nil)
	if st != 200 {
		t.Fatalf("accounts: %d %s", st, b)
	}
	var list []map[string]any
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatalf("accounts: %v: %s", err, b)
	}
	for _, a := range list {
		if a["id"] == id {
			return a
		}
	}
	t.Fatalf("account %s not listed: %s", id, b)
	return nil
}

func (e *p2Env) audit(t *testing.T) []map[string]any {
	t.Helper()
	_, m := e.obj(t, "GET", "/api/audit?limit=200", nil)
	var out []map[string]any
	for _, x := range m["entries"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func hasAudit(es []map[string]any, action, actor string) bool {
	for _, e := range es {
		if e["action"] == action && (actor == "" || e["actor"] == actor) {
			return true
		}
	}
	return false
}

func strEnv(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m["env"].(map[string]any) {
		out[k] = v.(string)
	}
	return out
}

func TestP2SpawnWorktreeBriefAndHello(t *testing.T) {
	e := p2Start(t, 0)
	res := e.mustSpawn(t, map[string]any{
		"account": "a1", "role": "implementer", "model": "m1", "task": "Write hello",
		"plan": []map[string]any{{"id": "s1", "title": "T", "acceptance": "A"}},
	})
	if err := proto.ValidateJSON("api.spawn", res); err != nil {
		t.Fatalf("spawn response schema: %v", err)
	}
	wt := res["worktree"].(string)
	if st, err := os.Stat(wt); err != nil || !st.IsDir() {
		t.Fatalf("worktree missing: %v", err)
	}
	branch := res["branch"].(string)
	if !strings.HasPrefix(branch, "baton/") {
		t.Fatalf("branch %q", branch)
	}
	if got := p2Git(t, e.repo, "branch", "--list", branch); !strings.Contains(got, branch) {
		t.Fatalf("branch not created: %q", got)
	}
	if _, err := os.Stat(filepath.Join(wt, "README.md")); err != nil {
		t.Fatalf("worktree has no checkout: %v", err)
	}
	env := strEnv(res)
	brief := env["BATON_BRIEF"]
	b, err := os.ReadFile(brief)
	if err != nil || !strings.Contains(string(b), "Write hello") {
		t.Fatalf("brief %q: %v", brief, err)
	}
	if env["BATON_URL"] != e.h.URL || env["BATON_RUN_ID"] != res["run_id"] {
		t.Fatalf("env: %v", env)
	}
	if r := baton(t, fastEnv(env), []string{"hello"}, ""); r.Code != 0 {
		t.Fatalf("baton hello: %+v", r)
	}
	// the hello reached the hub
	until(t, 5*time.Second, "hello event", func() (bool, bool) {
		for _, ev := range events(t, e.h.URL, e.tok, res["run_id"].(string)) {
			if ev.Type == "hello" {
				return true, true
			}
		}
		return false, false
	})
	sum := runSummary(t, e.h.URL, e.tok, res["run_id"].(string))
	if sum["account_id"] != "a1" || sum["role"] != "implementer" {
		t.Fatalf("summary: %v", sum)
	}
	// state carries accounts/mode/config_ok
	_, st := e.obj(t, "GET", "/api/state", nil)
	if st["mode"] != "propose" || st["config_ok"] != true || st["instance"] == "" {
		t.Fatalf("state: %v", st)
	}
	if a := e.account(t, "a1"); a["used"].(float64) != 1 || a["max_concurrent"].(float64) != 1 || a["rate_limited"] != false {
		t.Fatalf("account: %v", a)
	}
	_, rawAcc := e.call(t, "GET", "/api/accounts", nil, nil)
	var accV any
	_ = json.Unmarshal(rawAcc, &accV)
	if err := proto.ValidateJSON("api.accounts", accV); err != nil {
		t.Fatalf("accounts schema: %v", err)
	}
	if _, cfgM := e.obj(t, "GET", "/api/config", nil); proto.ValidateJSON("api.config", cfgM) != nil {
		t.Fatalf("config schema: %v", proto.ValidateJSON("api.config", cfgM))
	}
	_, auditM := e.obj(t, "GET", "/api/audit", nil)
	if err := proto.ValidateJSON("api.audit", auditM); err != nil {
		t.Fatalf("audit schema: %v", err)
	}
	if err := proto.ValidateJSON("api.state", st); err != nil {
		t.Fatalf("state schema: %v", err)
	}
	es := e.audit(t)
	if !hasAudit(es, "spawn", "human") {
		t.Fatalf("no spawn audit: %v", es)
	}
	// dry run: no worktree, no env
	dry := e.mustSpawn(t, map[string]any{"account": "a2", "role": "reviewer", "dry_run": true})
	if dry["dry_run"] != true || dry["env"] != nil {
		t.Fatalf("dry: %v", dry)
	}
	if _, err := os.Stat(dry["worktree"].(string)); err == nil {
		t.Fatal("dry run created a worktree")
	}
	// conductor actor via header
	body, _ := json.Marshal(map[string]any{"account": "a2", "role": "reviewer", "task": "review"})
	req, _ := http.NewRequest("POST", e.h.URL+"/api/spawn", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.tok)
	req.Header.Set("X-Baton-Actor", "conductor")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !hasAudit(e.audit(t), "spawn", "conductor") {
		t.Fatalf("conductor spawn: %d", resp.StatusCode)
	}
}

func TestP2SelectionErrors(t *testing.T) {
	e := p2Start(t, 0)
	cases := []struct {
		name   string
		req    map[string]any
		status int
		code   string
	}{
		{"no account no routing", map[string]any{"role": "implementer"}, 400, "ACCOUNT_REQUIRED"},
		{"unknown account", map[string]any{"account": "zz", "role": "implementer"}, 400, "UNKNOWN_ACCOUNT"},
		{"disabled", map[string]any{"account": "off", "role": "implementer"}, 409, "ACCOUNT_DISABLED"},
		{"role", map[string]any{"account": "a2", "role": "implementer"}, 409, "ROLE_NOT_ALLOWED"},
		{"model", map[string]any{"account": "a1", "role": "implementer", "model": "nope"}, 409, "MODEL_NOT_ALLOWED"},
	}
	for _, c := range cases {
		st, m := e.spawn(t, c.req)
		if st != c.status || m["code"] != c.code {
			t.Errorf("%s: got %d %v want %d %s", c.name, st, m, c.status, c.code)
		}
	}
	e.mustSpawn(t, map[string]any{"account": "a1", "role": "implementer", "callsign": "Falcon"})
	if st, m := e.spawn(t, map[string]any{"account": "a1", "role": "implementer"}); st != 409 || m["code"] != "ACCOUNT_BUSY" {
		t.Errorf("busy: %d %v", st, m)
	}
	if st, m := e.spawn(t, map[string]any{"account": "a2", "role": "reviewer", "callsign": "Falcon"}); st != 409 || m["code"] != "CALLSIGN_TAKEN" {
		t.Errorf("callsign: %d %v", st, m)
	}
	if st, m := e.obj(t, "POST", "/api/spawn", map[string]any{"account": "a1", "bogus": 1}); st != 400 || m["code"] != "INVALID_PAYLOAD" {
		t.Errorf("unknown field: %d %v", st, m)
	}
}

func TestP2PatchAccountAndHubConfig(t *testing.T) {
	e := p2Start(t, 0)
	e.mustSpawn(t, map[string]any{"account": "a1", "role": "implementer"})
	if st, m := e.spawn(t, map[string]any{"account": "a1", "role": "tester"}); st != 409 || m["code"] != "ACCOUNT_BUSY" {
		t.Fatalf("expected busy: %d %v", st, m)
	}
	st, m := e.obj(t, "PATCH", "/api/accounts/a1", map[string]any{"max_concurrent": 2})
	if st != 200 || m["max_concurrent"].(float64) != 2 || m["used"].(float64) != 1 {
		t.Fatalf("patch: %d %v", st, m)
	}
	b, _ := os.ReadFile(e.cfg)
	s := string(b)
	if !strings.Contains(s, "max_concurrent: 2") || !strings.Contains(s, "# main login") || !strings.Contains(s, "# my accounts") {
		t.Fatalf("yaml after patch:\n%s", s)
	}
	e.mustSpawn(t, map[string]any{"account": "a1", "role": "tester"})
	if a := e.account(t, "a1"); a["used"].(float64) != 2 {
		t.Fatalf("used: %v", a)
	}
	// refused edits
	st, m = e.obj(t, "PATCH", "/api/accounts/a1", map[string]any{"max_concurrent": -3})
	if st != 400 || m["code"] != "CONFIG_INVALID" {
		t.Fatalf("invalid patch: %d %v", st, m)
	}
	// all or nothing: "enabled" (applied first) is rolled back when "max_concurrent" is refused
	before, _ := os.ReadFile(e.cfg)
	if st, m = e.obj(t, "PATCH", "/api/accounts/a1", map[string]any{"enabled": false, "max_concurrent": -3}); st != 400 || m["code"] != "CONFIG_INVALID" {
		t.Fatalf("mixed patch: %d %v", st, m)
	}
	if after, _ := os.ReadFile(e.cfg); string(after) != string(before) {
		t.Fatalf("mixed patch left a partial write:\n%s", after)
	}
	if a := e.account(t, "a1"); a["enabled"] != true {
		t.Fatalf("a1 after refused mixed patch: %v", a)
	}
	if st, m = e.obj(t, "PATCH", "/api/accounts/a1", map[string]any{"nope": 1}); st != 400 || m["code"] != "CONFIG_INVALID" {
		t.Fatalf("unknown field: %d %v", st, m)
	}
	if st, m = e.obj(t, "PATCH", "/api/accounts/zz", map[string]any{"enabled": false}); st != 404 {
		t.Fatalf("unknown account: %d %v", st, m)
	}
	if st, m = e.obj(t, "PATCH", "/api/accounts/a2", map[string]any{"roles": []string{"reviewer", "tester"}, "enabled": false}); st != 200 || m["enabled"] != false {
		t.Fatalf("roles/enabled: %d %v", st, m)
	}
	if st, m = e.spawn(t, map[string]any{"account": "a2", "role": "reviewer"}); st != 409 || m["code"] != "ACCOUNT_DISABLED" {
		t.Fatalf("disabled after patch: %d %v", st, m)
	}
	// hub config
	st, m = e.obj(t, "PATCH", "/api/config", map[string]any{"mode": "auto", "worktrees.cleanup": "remove_on_merge"})
	if st != 200 || m["ok"] != true {
		t.Fatalf("patch hub: %d %v", st, m)
	}
	hubCfg := m["config"].(map[string]any)["hub"].(map[string]any)
	if hubCfg["mode"] != "auto" || hubCfg["worktrees"].(map[string]any)["cleanup"] != "remove_on_merge" {
		t.Fatalf("hub cfg: %v", hubCfg)
	}
	// conductor section through the same route
	st, m = e.obj(t, "PATCH", "/api/config", map[string]any{"conductor.effort": "medium", "conductor.model": "sonnet"})
	if c := m["config"].(map[string]any)["conductor"].(map[string]any); st != 200 || c["effort"] != "medium" || c["model"] != "sonnet" {
		t.Fatalf("patch conductor: %d %v", st, m)
	}
	if st, m = e.obj(t, "PATCH", "/api/config", map[string]any{"conductor.nope": "x"}); st != 400 || m["code"] != "CONFIG_INVALID" {
		t.Fatalf("bad conductor key: %d %v", st, m)
	}
	if st, m = e.obj(t, "PATCH", "/api/config", map[string]any{"mode": "chaos"}); st != 400 || m["code"] != "CONFIG_INVALID" {
		t.Fatalf("bad mode: %d %v", st, m)
	}
	if st, m = e.obj(t, "PATCH", "/api/config", map[string]any{"nope": true}); st != 400 || m["code"] != "CONFIG_INVALID" {
		t.Fatalf("bad key: %d %v", st, m)
	}
	b, _ = os.ReadFile(e.cfg)
	if !strings.Contains(string(b), "# keep") {
		t.Fatalf("hub comment lost:\n%s", b)
	}
	es := e.audit(t)
	if !hasAudit(es, "config.account", "human") || !hasAudit(es, "config.hub", "human") {
		t.Fatalf("audit: %v", es)
	}
	var found bool
	for _, x := range es {
		if x["action"] == "config.account" && x["target"] == "a1" {
			d := x["detail"].(map[string]any)
			found = d["field"] == "max_concurrent" && d["file"] == e.cfg
		}
	}
	if !found {
		t.Fatalf("config.account detail wrong: %v", es)
	}
}

func TestP2BrokenConfigKeepsServing(t *testing.T) {
	e := p2Start(t, 0)
	req, _ := http.NewRequest("GET", e.h.URL+"/api/stream", nil)
	req.Header.Set("Authorization", "Bearer "+e.tok)
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	waitFor := func(sub string) string {
		t.Helper()
		end := time.After(8 * time.Second)
		for {
			select {
			case l := <-lines:
				if strings.Contains(l, sub) {
					return l
				}
			case <-end:
				t.Fatalf("timeout waiting for %q", sub)
			}
		}
	}
	waitFor("event: hello")

	good, _ := os.ReadFile(e.cfg)
	if err := os.WriteFile(e.cfg, []byte("hub:\n  port: 99999\naccounts: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := waitFor(`"type":"config_reloaded"`)
	if !strings.Contains(l, `"ok":false`) {
		t.Fatalf("notice: %s", l)
	}
	var notice any
	_ = json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &notice)
	if err := proto.ValidateJSON("api.notice", notice); err != nil {
		t.Fatalf("notice schema: %v", err)
	}
	st, m := e.obj(t, "GET", "/api/config", nil)
	if st != 200 || m["ok"] != false || len(m["issues"].([]any)) == 0 {
		t.Fatalf("config: %d %v", st, m)
	}
	// hub still serves and the old accounts are still in effect
	if a := e.account(t, "a1"); a["max_concurrent"].(float64) != 1 {
		t.Fatalf("old config lost: %v", a)
	}
	e.mustSpawn(t, map[string]any{"account": "a1", "role": "implementer"})
	if _, s := e.obj(t, "GET", "/api/state", nil); s["config_ok"] != false || len(s["config_issues"].([]any)) == 0 {
		t.Fatalf("state: %v", s)
	}
	// fix it
	if err := os.WriteFile(e.cfg, good, 0o644); err != nil {
		t.Fatal(err)
	}
	if l := waitFor(`"type":"config_reloaded"`); !strings.Contains(l, `"ok":true`) {
		t.Fatalf("notice: %s", l)
	}
	if _, m = e.obj(t, "GET", "/api/config", nil); m["ok"] != true || len(m["issues"].([]any)) != 0 {
		t.Fatalf("config after fix: %v", m)
	}
}

func TestP2DeliveryAuditAndShutdown(t *testing.T) {
	e := p2Start(t, 0)
	res := e.mustSpawn(t, map[string]any{"account": "a1", "role": "implementer"})
	rid := res["run_id"].(string)
	for _, typ := range []string{"message", "pause", "resume"} {
		body := map[string]any{"text": "x"}
		if typ != "message" {
			body = map[string]any{}
		}
		if st, m := e.obj(t, "POST", "/api/runs/"+rid+"/"+typ, body); st != 200 {
			t.Fatalf("%s: %d %v", typ, st, m)
		}
	}
	es := e.audit(t)
	for _, a := range []string{"deliver.message", "deliver.pause", "deliver.resume"} {
		if !hasAudit(es, a, "human") {
			t.Errorf("missing audit %s: %v", a, es)
		}
	}
	if st, _ := e.obj(t, "POST", "/api/hub/shutdown", nil); st != 200 {
		t.Fatal("shutdown")
	}
	select {
	case <-e.h.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("hub did not stop")
	}
	if !hasAudit(readAuditFile(t, e.data), "hub.shutdown", "human") {
		t.Fatal("hub.shutdown not in audit.jsonl")
	}
}

func readAuditFile(t *testing.T, dataDir string) []map[string]any {
	t.Helper()
	return readJSONL(t, filepath.Join(dataDir, "audit.jsonl"))
}

func TestP2IdleStopWithLiveRunAndStream(t *testing.T) {
	e := p2Start(t, 400*time.Millisecond)
	res := e.mustSpawn(t, map[string]any{"account": "a1", "role": "implementer"})
	select {
	case <-e.h.Done():
		t.Fatal("hub stopped with a live run")
	case <-time.After(1500 * time.Millisecond):
	}
	// end the run: done via the CLI, then the hub must idle out
	env := fastEnv(strEnv(res))
	baton(t, env, []string{"hello"}, "")
	if r := baton(t, env, []string{"done", "--status", "success", "--summary", "ok"}, ""); r.Code != 0 {
		t.Fatalf("baton done: %+v", r)
	}
	select {
	case <-e.h.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("idle hub did not stop after the run ended")
	}
}
