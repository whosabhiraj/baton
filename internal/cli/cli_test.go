package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/hub"
)

type run struct {
	t   *testing.T
	dir string
	env map[string]string
}

func newRun(t *testing.T, extra ...string) *run {
	root := t.TempDir()
	r := &run{t: t, dir: filepath.Join(root, "run"), env: map[string]string{
		"BATON_RUN_ID": "r_test", "BATON_AGENT_ID": "cc-a-0001", "BATON_RUN_DIR": filepath.Join(root, "run"),
		"BATON_URL": "http://127.0.0.1:9", "BATON_TOKEN": "bt_run_x",
	}}
	for i := 0; i+1 < len(extra); i += 2 {
		r.env[extra[i]] = extra[i+1]
	}
	return r
}

func (r *run) do(stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(stdin), &out, &errb, func(k string) string { return r.env[k] })
	return code, strings.TrimSpace(out.String()), strings.TrimSpace(errb.String())
}

func (r *run) outbox() []map[string]any {
	b, err := os.ReadFile(filepath.Join(r.dir, "outbox.jsonl"))
	if err != nil {
		return nil
	}
	var res []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			r.t.Fatalf("bad outbox line %q", l)
		}
		res = append(res, m)
	}
	return res
}

func (r *run) put(n int, typ string, p map[string]any) {
	_ = os.MkdirAll(filepath.Join(r.dir, "inbox"), 0o755)
	b, _ := json.Marshal(map[string]any{"v": 1, "n": n, "ts": "t", "type": typ, "payload": p})
	if err := os.WriteFile(filepath.Join(r.dir, "inbox", fmt.Sprintf("%06d.json", n)), b, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *run) readLines() []string {
	b, _ := os.ReadFile(filepath.Join(r.dir, "inbox", ".read"))
	return strings.Fields(string(b))
}

func TestSpoolEveryCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		typ  string
		want map[string]any
	}{
		{"hello", []string{"hello"}, "hello", map[string]any{"brief_ack": true}},
		{"step start", []string{"step", "s1", "start", "--note", "go"}, "step_start", map[string]any{"step_id": "s1", "note": "go"}},
		{"step flags first", []string{"--note=x", "step", "s1", "start"}, "step_start", map[string]any{"step_id": "s1", "note": "x"}},
		{"step done", []string{"step", "s1", "done", "--summary", "ok", "--evidence", "abc"}, "step_done", map[string]any{"step_id": "s1", "summary": "ok", "evidence": "abc"}},
		{"step done flags after", []string{"step", "s1", "done", "--summary", "x"}, "step_done", map[string]any{"summary": "x"}},
		{"step blocked", []string{"step", "s1", "blocked", "--reason", "stuck"}, "step_blocked", map[string]any{"reason": "stuck"}},
		{"progress", []string{"progress", "40", "half"}, "progress", map[string]any{"percent": 40.0, "note": "half"}},
		{"progress frac", []string{"progress", "12.5", "n"}, "progress", map[string]any{"percent": 12.5}},
		{"progress no note", []string{"progress", "0"}, "progress", map[string]any{"percent": 0.0, "note": ""}},
		{"artifact", []string{"artifact", "commit", "abc123", "--note", "n"}, "artifact", map[string]any{"kind": "commit", "ref": "abc123", "note": "n"}},
		{"ask", []string{"ask", "which db?", "--options", "pg, sqlite,,"}, "question", map[string]any{"text": "which db?", "blocking": false}},
		{"heartbeat", []string{"heartbeat"}, "heartbeat", map[string]any{}},
		{"done", []string{"done", "--status", "partial", "--summary", "half", "--followups", "more"}, "done", map[string]any{"status": "partial", "summary": "half", "followups": "more"}},
		{"error", []string{"error", "boom", "--fatal"}, "error", map[string]any{"message": "boom", "fatal": true}},
		{"error nonfatal", []string{"error", "boom"}, "error", map[string]any{"fatal": false}},
		{"plan confirm", []string{"plan", "--confirm"}, "plan", map[string]any{"confirm": true}},
		{"dashdash", []string{"error", "--", "--weird"}, "error", map[string]any{"message": "--weird"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRun(t, "BATON_MODEL", "opus")
			code, out, errs := r.do("", c.args...)
			if code != 0 {
				t.Fatalf("code %d err %s", code, errs)
			}
			wantOut := "spooled (hub unreachable)"
			if c.typ == "question" {
				if !regexp.MustCompile(`^qid q_[0-9a-z]{6}$`).MatchString(out) {
					t.Fatalf("out %q", out)
				}
			} else if out != wantOut {
				t.Fatalf("out %q", out)
			}
			ob := r.outbox()
			if len(ob) != 1 {
				t.Fatalf("outbox %v", ob)
			}
			m := ob[0]
			if m["type"] != c.typ || m["v"] != 1.0 || m["run_id"] != "r_test" || m["agent_id"] != "cc-a-0001" {
				t.Fatalf("envelope %v", m)
			}
			if !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(m["msg_id"].(string)) {
				t.Fatal("msg_id")
			}
			if _, err := time.Parse("2006-01-02T15:04:05.000Z", m["ts"].(string)); err != nil {
				t.Fatal(err)
			}
			p := m["payload"].(map[string]any)
			for k, v := range c.want {
				if fmt.Sprint(p[k]) != fmt.Sprint(v) {
					t.Errorf("payload[%s]=%v want %v", k, p[k], v)
				}
			}
			if c.name == "ask" {
				o := p["options"].([]any)
				if len(o) != 2 || o[0] != "pg" || o[1] != "sqlite" {
					t.Errorf("options %v", o)
				}
			}
		})
	}
}

func TestHelloCwdModel(t *testing.T) {
	r := newRun(t, "BATON_MODEL", "opus")
	r.do("", "hello")
	p := r.outbox()[0]["payload"].(map[string]any)
	cwd, _ := os.Getwd()
	if p["model"] != "opus" || p["cwd"] != cwd {
		t.Fatal(p)
	}
}

func TestSpoolWithoutURLToken(t *testing.T) {
	r := newRun(t)
	delete(r.env, "BATON_URL")
	delete(r.env, "BATON_TOKEN")
	if code, _, _ := r.do("", "heartbeat"); code != 0 || len(r.outbox()) != 1 {
		t.Fatal("not spooled")
	}
}

func TestPlanInputs(t *testing.T) {
	steps := `[{"id":"s1","title":"T","acceptance":"A"}]`
	a := newRun(t)
	if code, _, e := a.do(`{"steps":`+steps+`,"reason":"r"}`, "plan"); code != 0 {
		t.Fatal(e)
	}
	p := a.outbox()[0]["payload"].(map[string]any)
	if p["reason"] != "r" || len(p["steps"].([]any)) != 1 {
		t.Fatal(p)
	}
	b := newRun(t)
	if code, _, e := b.do(steps, "plan"); code != 0 {
		t.Fatal(e)
	}
	if len(b.outbox()[0]["payload"].(map[string]any)["steps"].([]any)) != 1 {
		t.Fatal("bare array")
	}
	c := newRun(t)
	f := filepath.Join(t.TempDir(), "plan.json")
	_ = os.WriteFile(f, []byte(steps), 0o644)
	if code, _, e := c.do("", "plan", "--file", f); code != 0 {
		t.Fatal(e)
	}
	if len(c.outbox()) != 1 {
		t.Fatal("file")
	}
}

func TestUsageErrorsExit2(t *testing.T) {
	bad := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"bad percent", []string{"progress", "101", "x"}, ""},
		{"neg percent", []string{"progress", "-5", "x"}, ""},
		{"neg percent after --", []string{"progress", "--", "-5"}, ""},
		{"non-numeric percent", []string{"progress", "abc", "x"}, ""},
		{"missing percent", []string{"progress"}, ""},
		{"bad status", []string{"done", "--status", "great", "--summary", "x"}, ""},
		{"missing status", []string{"done", "--summary", "x"}, ""},
		{"missing summary", []string{"step", "s1", "done"}, ""},
		{"missing reason", []string{"step", "s1", "blocked"}, ""},
		{"missing step id", []string{"step"}, ""},
		{"missing action", []string{"step", "s1"}, ""},
		{"bad step action", []string{"step", "s1", "jump"}, ""},
		{"bad artifact kind", []string{"artifact", "zip", "x"}, ""},
		{"missing ref", []string{"artifact", "file"}, ""},
		{"bad plan json", []string{"plan"}, "{nope"},
		{"empty plan", []string{"plan"}, ""},
		{"bad plan shape", []string{"plan"}, `{"steps":[]}`},
		{"plan scalar", []string{"plan"}, `5`},
		{"plan file missing", []string{"plan", "--file", "/no/such/file"}, ""},
		{"unknown command", []string{"frobnicate"}, ""},
		{"unknown flag", []string{"hello", "--wat"}, ""},
		{"unknown short", []string{"hello", "-x"}, ""},
		{"bool with value", []string{"error", "x", "--fatal=1"}, ""},
		{"flag missing value", []string{"error", "x", "--note"}, ""},
		{"flag ambiguous value", []string{"artifact", "file", "x", "--note", "-y"}, ""},
		{"extra positional", []string{"hello", "extra"}, ""},
		{"extra positional step", []string{"step", "a", "start", "b"}, ""},
		{"ask missing text", []string{"ask"}, ""},
		{"ask bad timeout", []string{"ask", "q", "--blocking", "--timeout", "abc"}, ""},
		{"ask neg timeout", []string{"ask", "q", "--timeout=-1"}, ""},
		{"error missing msg", []string{"error"}, ""},
		{"hub no sub", []string{"hub"}, ""},
		{"hub bad sub", []string{"hub", "nope"}, ""},
		{"serve bad port", []string{"hub", "serve", "--port", "abc", "--data-dir", "x"}, ""},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			r := newRun(t)
			code, _, e := r.do(c.stdin, c.args...)
			if code != 2 || !strings.HasPrefix(e, "baton: ") {
				t.Fatalf("code %d err %q", code, e)
			}
			if len(r.outbox()) != 0 {
				t.Fatal("spooled on usage error")
			}
		})
	}
}

func TestLaterPhaseMessage(t *testing.T) {
	for cmd, ph := range laterPhase {
		_, _, e := newRun(t).do("", cmd)
		if e != fmt.Sprintf("baton: %s not available yet (Phase %d)", cmd, ph) {
			t.Fatal(e)
		}
	}
}

func TestMissingEnv(t *testing.T) {
	r := newRun(t)
	delete(r.env, "BATON_RUN_ID")
	code, _, e := r.do("", "hello")
	if code != 2 || !strings.HasPrefix(e, "baton: not inside a Baton run") {
		t.Fatal(code, e)
	}
	// env is checked before missing args
	code, _, e = r.do("", "progress")
	if code != 2 || !strings.Contains(e, "not inside") {
		t.Fatal(e)
	}
}

func TestHelpVersionNoEnv(t *testing.T) {
	r := &run{t: t, env: map[string]string{}}
	for _, a := range [][]string{{"help"}, {"--help"}, {"-h"}, {"hello", "--help"}} {
		code, out, e := r.do("", a...)
		if code != 0 || !strings.HasPrefix(out, "usage: baton") || e != "" {
			t.Fatalf("%v: %d %q %q", a, code, out, e)
		}
	}
	if code, out, _ := r.do("", "version"); code != 0 || out != "baton dev" {
		t.Fatal(out)
	}
}

func TestInboxReadMark(t *testing.T) {
	r := newRun(t)
	r.put(3, "answer", map[string]any{"qid": "q_ab12cd", "text": "use postgres"})
	r.put(4, "message", map[string]any{"from": "conductor", "text": "rebase\n  onto main"})
	r.put(5, "plan_update", map[string]any{"steps": []any{map[string]any{"id": "s1", "title": "One"}, map[string]any{"id": "s2", "title": "Two"}}})
	r.put(6, "stop", map[string]any{"reason": "enough"})
	r.put(9, "pause", map[string]any{})
	_ = os.WriteFile(filepath.Join(r.dir, "inbox", "000007.json.tmp"), []byte("{"), 0o644)
	_ = os.WriteFile(filepath.Join(r.dir, "inbox", "junk.json"), []byte("{"), 0o644)
	_ = os.WriteFile(filepath.Join(r.dir, "inbox", "000008.json"), []byte("{broken"), 0o644)
	code, out, _ := r.do("", "inbox")
	want := strings.Join([]string{
		"[3] answer q_ab12cd: use postgres", "[4] message (conductor): rebase onto main",
		"[5] plan_update: s1 One; s2 Two", "[6] stop: enough", "[9] pause",
	}, "\n")
	if code != 0 || out != want {
		t.Fatalf("got %q", out)
	}
	if got := strings.Join(r.readLines(), ","); got != "3,4,5,6,9" {
		t.Fatal(got)
	}
	if _, out, _ := r.do("", "inbox"); out != "inbox empty" {
		t.Fatal(out)
	}
	r.put(10, "resume", map[string]any{})
	if _, out, _ := r.do("", "inbox"); out != "[10] resume" {
		t.Fatal(out)
	}
}

func TestInboxEmptyNoInboxDir(t *testing.T) {
	r := newRun(t)
	_ = os.MkdirAll(r.dir, 0o755)
	if _, out, _ := r.do("", "inbox"); out != "inbox empty" {
		t.Fatal(out)
	}
}

func TestAskNoAnswer(t *testing.T) {
	r := newRun(t, "BATON_POLL_INTERVAL_S", "0.05")
	start := time.Now()
	code, out, _ := r.do("", "ask", "q?", "--blocking", "--timeout", "0.3")
	if code != 0 || out != "NO_ANSWER" || time.Since(start) < 250*time.Millisecond {
		t.Fatal(code, out)
	}
	if r.outbox()[0]["payload"].(map[string]any)["blocking"] != true {
		t.Fatal("blocking")
	}
}

func TestAskEnvTimeoutAndBadEnv(t *testing.T) {
	r := newRun(t, "BATON_POLL_INTERVAL_S", "0.05", "BATON_ASK_TIMEOUT_S", "0.2")
	if _, out, _ := r.do("", "ask", "q", "--blocking"); out != "NO_ANSWER" {
		t.Fatal(out)
	}
	if got := num("garbage", 900); got != 900 {
		t.Fatal(got)
	}
	if got := num("-3", 900); got != 900 {
		t.Fatal(got)
	}
	if got := num("0.5", 900); got != 0.5 {
		t.Fatal(got)
	}
}

func TestAskBlockingConsumesOnlyMatching(t *testing.T) {
	r := newRun(t, "BATON_POLL_INTERVAL_S", "0.05")
	type res struct {
		code int
		out  string
	}
	ch := make(chan res, 1)
	go func() {
		c, o, _ := r.do("", "ask", "which db?", "--blocking", "--timeout", "10")
		ch <- res{c, o}
	}()
	var qid string
	for i := 0; i < 100 && qid == ""; i++ {
		time.Sleep(20 * time.Millisecond)
		if ob := r.outbox(); len(ob) > 0 {
			qid = ob[0]["payload"].(map[string]any)["qid"].(string)
		}
	}
	if qid == "" {
		t.Fatal("no question spooled")
	}
	r.put(1, "answer", map[string]any{"qid": "q_other1", "text": "not mine"})
	r.put(2, "message", map[string]any{"from": "human", "text": "hi"})
	r.put(3, "answer", map[string]any{"qid": qid, "text": "postgres"})
	select {
	case v := <-ch:
		if v.code != 0 || v.out != "postgres" {
			t.Fatal(v)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timeout")
	}
	if got := strings.Join(r.readLines(), ","); got != "3" {
		t.Fatal(got)
	}
	if _, out, _ := r.do("", "inbox"); out != "[1] answer q_other1: not mine\n[2] message (human): hi" {
		t.Fatal(out)
	}
}

func TestAskHeartbeats(t *testing.T) {
	r := newRun(t, "BATON_POLL_INTERVAL_S", "0.02", "BATON_HEARTBEAT_INTERVAL_S", "0.1")
	r.do("", "ask", "q?", "--blocking", "--timeout", "0.5")
	n := 0
	for _, m := range r.outbox() {
		if m["type"] == "heartbeat" {
			n++
		}
	}
	if n < 1 {
		t.Fatal("no heartbeat")
	}
}

// ---- fake hub ----

type fakeHub struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	reply  string
	seen   []seenReq
}
type seenReq struct {
	auth, path string
	body       map[string]any
}

func newFakeHub(t *testing.T) *fakeHub {
	f := &fakeHub{status: 200, reply: "{}"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.seen = append(f.seen, seenReq{r.Header.Get("authorization"), r.URL.RequestURI(), body})
		st, rp := f.status, f.reply
		f.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write([]byte(rp))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeHub) set(st int, reply string) {
	f.mu.Lock()
	f.status, f.reply = st, reply
	f.mu.Unlock()
}

func TestFakeHub(t *testing.T) {
	f := newFakeHub(t)
	cases := []struct {
		name    string
		st      int
		reply   string
		code    int
		out     string
		errRe   string
		spooled bool
	}{
		{"200", 200, `{"ok":true,"seq":7,"duplicate":false}`, 0, "ok #7", "", false},
		{"dup", 200, `{"ok":true,"seq":8,"duplicate":true}`, 0, "ok #8 (dup)", "", false},
		{"200 no body", 200, ``, 0, "ok #?", "", false},
		{"500", 500, `{"ok":false,"code":"X","error":"x"}`, 0, "spooled (hub unreachable)", "", true},
		{"410", 410, `{"ok":false,"code":"RUN_ENDED","error":"ended"}`, 0, "", `^baton: rejected: RUN_ENDED$`, false},
		{"401", 401, `{"ok":false,"code":"UNAUTHORIZED","error":"no"}`, 0, "", `rejected: UNAUTHORIZED`, false},
		{"403 no body", 403, ``, 0, "", `^baton: rejected: 403$`, false},
		{"400", 400, `{"ok":false,"code":"INVALID_PAYLOAD","error":"bad"}`, 2, "", `^baton: rejected: INVALID_PAYLOAD bad$`, false},
		{"400 no body", 400, ``, 2, "", `^baton: rejected: 400$`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.set(c.st, c.reply)
			r := newRun(t, "BATON_URL", f.URL+"/")
			code, out, e := r.do("", "progress", "10", "x")
			if code != c.code || out != c.out {
				t.Fatalf("code %d out %q err %q", code, out, e)
			}
			if c.errRe != "" && !regexp.MustCompile(c.errRe).MatchString(e) {
				t.Fatalf("err %q", e)
			}
			if (len(r.outbox()) == 1) != c.spooled {
				t.Fatalf("spooled=%v", len(r.outbox()))
			}
			f.mu.Lock()
			last := f.seen[len(f.seen)-1]
			f.mu.Unlock()
			if last.auth != "Bearer bt_run_x" || last.path != "/api/ingest" || last.body["type"] != "progress" || last.body["msg_id"] == "" {
				t.Fatalf("request %+v", last)
			}
		})
	}
}

func TestSpoolMsgIDMatchesPost(t *testing.T) {
	f := newFakeHub(t)
	f.set(500, "{}")
	r := newRun(t, "BATON_URL", f.URL)
	r.do("", "heartbeat")
	if f.seen[0].body["msg_id"] != r.outbox()[0]["msg_id"] {
		t.Fatal("msg_id differs between POST and spool")
	}
}

func TestInboxAPIFallback(t *testing.T) {
	f := newFakeHub(t)
	f.set(200, `{"ok":true,"messages":[{"v":1,"n":2,"ts":"t","type":"message","payload":{"from":"human","text":"yo"}}]}`)
	r := newRun(t, "BATON_URL", f.URL)
	_, out, _ := r.do("", "inbox")
	if out != "[2] message (human): yo" {
		t.Fatal(out)
	}
	if f.seen[0].path != "/api/runs/r_test/inbox?after=0" || f.seen[0].auth != "Bearer bt_run_x" {
		t.Fatalf("%+v", f.seen[0])
	}
}

func TestInboxLocalPreferredWhenRunDirExists(t *testing.T) {
	f := newFakeHub(t)
	r := newRun(t, "BATON_URL", f.URL)
	_ = os.MkdirAll(r.dir, 0o755)
	r.do("", "inbox")
	if len(f.seen) != 0 {
		t.Fatal("API used although run dir exists")
	}
}

func TestParseArgs(t *testing.T) {
	o, pos, err := parseArgs([]string{"a", "--note=x=y", "-", "--note", "z", "b", "--", "--c"})
	if err != nil || o.get("note") != "z" || strings.Join(pos, ",") != "a,-,b,--c" {
		t.Fatal(o, pos, err)
	}
	if o, _, _ := parseArgs([]string{"--note="}); !o.has("note") {
		t.Fatal("empty value should be defined")
	}
	if o, _, err := parseArgs([]string{"-hh"}); err != nil || !o.flag("help") {
		t.Fatal(err)
	}
	if _, _, err := parseArgs([]string{"--NOTE", "x"}); err == nil {
		t.Fatal("case sensitive")
	}
}

// ---- up ----

func TestHubServe(t *testing.T) {
	oldCfg := loadConfig
	defer func() { loadConfig = oldCfg }()
	loadConfig = func(string) *config.Snapshot {
		return &config.Snapshot{Config: config.Config{Hub: config.Hub{Port: 7777}}}
	}
	var got hub.Options
	oldStart, oldClose, oldCtx := startHub, closeHub, upStopCtx
	defer func() { startHub, closeHub, upStopCtx = oldStart, oldClose, oldCtx }()
	closed := false
	startHub = func(_ context.Context, o hub.Options) (*hub.Hub, error) {
		got = o
		return &hub.Hub{URL: "http://127.0.0.1:7777", AdminToken: "bt_adm_t", DataDir: o.DataDir, Port: 7777}, nil
	}
	closeHub = func(*hub.Hub, context.Context) error { closed = true; return nil }
	stopCtx, stop := context.WithCancel(context.Background())
	upStopCtx = func() (context.Context, context.CancelFunc) { return stopCtx, stop }
	tokFile := filepath.Join(t.TempDir(), "tok")
	dd := t.TempDir()
	done := make(chan struct{})
	var out, errb bytes.Buffer
	go func() {
		Main([]string{"hub", "serve", "--dev", "--data-dir", dd, "--admin-token-file", tokFile}, strings.NewReader(""), &out, &errb, func(string) string { return "" })
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	stop()
	<-done
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "baton hub: http://127.0.0.1:7777" || lines[1] != "admin token: bt_adm_t" || lines[2] != "data dir: "+dd {
		t.Fatal(lines)
	}
	if !got.Dev || got.Port != 7777 || !got.PortFallback || got.DataDir != dd || !closed {
		t.Fatalf("%+v closed=%v", got, closed)
	}
	if b, _ := os.ReadFile(tokFile); string(b) != "bt_adm_t" {
		t.Fatalf("token file %q", b)
	}

	// explicit --port disables fallback
	stopCtx2, stop2 := context.WithCancel(context.Background())
	stop2()
	upStopCtx = func() (context.Context, context.CancelFunc) { return stopCtx2, stop2 }
	Main([]string{"hub", "serve", "--port", "9100", "--data-dir", dd}, strings.NewReader(""), &out, &errb, func(string) string { return "" })
	if got.Port != 9100 || got.PortFallback {
		t.Fatalf("%+v", got)
	}

	// already running
	startHub = func(context.Context, hub.Options) (*hub.Hub, error) { return nil, hub.ErrAlreadyRunning }
	errb.Reset()
	if code := Main([]string{"hub", "serve", "--data-dir", dd}, strings.NewReader(""), &out, &errb, func(string) string { return "" }); code != 1 || !strings.Contains(errb.String(), "already running") {
		t.Fatal(code, errb.String())
	}
}
