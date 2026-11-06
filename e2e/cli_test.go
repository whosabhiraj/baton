package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Port of test/cli.test.ts (38 cases) against the Go binary.

type runFx struct {
	dir string
	env map[string]string
}

func newRun(t *testing.T, extra map[string]string) *runFx {
	t.Helper()
	requireBin(t)
	dir := filepath.Join(t.TempDir(), "run")
	env := map[string]string{
		"BATON_RUN_ID": "r_test", "BATON_AGENT_ID": "cc-a-0001", "BATON_RUN_DIR": dir,
		"BATON_URL": "http://127.0.0.1:9", "BATON_TOKEN": "bt_run_x",
	}
	for k, v := range extra {
		env[k] = v
	}
	return &runFx{dir: dir, env: env}
}

func (r *runFx) outbox(t *testing.T) []map[string]any {
	return readJSONL(t, filepath.Join(r.dir, "outbox.jsonl"))
}

func putInbox(t *testing.T, dir string, n int, typ string, payload any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"v": 1, "n": n, "ts": time.Now().UTC().Format(time.RFC3339), "type": typ, "payload": payload})
	if err := os.WriteFile(filepath.Join(dir, "inbox", fmt.Sprintf("%06d.json", n)), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestCLISpoolMode(t *testing.T) {
	type tc struct {
		name     string
		args     []string
		typ      string
		expected map[string]any
	}
	cases := []tc{
		{"hello", []string{"hello"}, "hello", map[string]any{"brief_ack": true}},
		{"step start", []string{"step", "s1", "start", "--note", "go"}, "step_start", map[string]any{"step_id": "s1", "note": "go"}},
		{"step done", []string{"step", "s1", "done", "--summary", "ok", "--evidence", "abc"}, "step_done", map[string]any{"step_id": "s1", "summary": "ok", "evidence": "abc"}},
		{"step blocked", []string{"step", "s1", "blocked", "--reason", "stuck"}, "step_blocked", map[string]any{"step_id": "s1", "reason": "stuck"}},
		{"progress", []string{"progress", "40", "half"}, "progress", map[string]any{"percent": 40.0, "note": "half"}},
		{"artifact", []string{"artifact", "commit", "abc123", "--note", "n"}, "artifact", map[string]any{"kind": "commit", "ref": "abc123", "note": "n"}},
		{"ask", []string{"ask", "which db?", "--options", "pg,sqlite"}, "question", map[string]any{"text": "which db?", "options": []any{"pg", "sqlite"}, "blocking": false}},
		{"heartbeat", []string{"heartbeat"}, "heartbeat", map[string]any{}},
		{"done", []string{"done", "--status", "partial", "--summary", "half", "--followups", "more"}, "done", map[string]any{"status": "partial", "summary": "half", "followups": "more"}},
		{"error", []string{"error", "boom", "--fatal"}, "error", map[string]any{"message": "boom", "fatal": true}},
		{"plan --confirm", []string{"plan", "--confirm"}, "plan", map[string]any{"confirm": true}},
	}
	uuidRe := regexp.MustCompile(`^[0-9a-f-]{36}$`)
	qidRe := regexp.MustCompile(`^qid q_[0-9a-z]{6}$`)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRun(t, nil)
			res := baton(t, r.env, c.args, "")
			if res.Code != 0 {
				t.Fatalf("exit %d: %s", res.Code, res.Err)
			}
			if c.name == "ask" {
				if !qidRe.MatchString(res.Out) {
					t.Fatalf("out = %q", res.Out)
				}
			} else if !strings.HasPrefix(res.Out, "spooled") {
				t.Fatalf("out = %q", res.Out)
			}
			ob := r.outbox(t)
			if len(ob) != 1 {
				t.Fatalf("outbox has %d lines", len(ob))
			}
			m := ob[0]
			if m["type"] != c.typ || m["v"] != 1.0 || m["run_id"] != "r_test" || m["agent_id"] != "cc-a-0001" {
				t.Fatalf("envelope = %v", m)
			}
			if id, _ := m["msg_id"].(string); !uuidRe.MatchString(id) {
				t.Fatalf("msg_id = %v", m["msg_id"])
			}
			if ts, _ := m["ts"].(string); ts == "" {
				t.Fatal("ts missing")
			} else if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
				t.Fatalf("ts %q: %v", ts, err)
			}
			payload := m["payload"].(map[string]any)
			got := map[string]any{}
			for k := range c.expected {
				if v, ok := payload[k]; ok {
					got[k] = v
				}
			}
			if !reflect.DeepEqual(got, c.expected) {
				t.Fatalf("payload subset = %v, want %v", got, c.expected)
			}
		})
	}

	t.Run("hello includes cwd and model", func(t *testing.T) {
		r := newRun(t, map[string]string{"BATON_MODEL": "opus"})
		baton(t, r.env, []string{"hello"}, "")
		p := r.outbox(t)[0]["payload"].(map[string]any)
		if p["model"] != "opus" {
			t.Fatalf("model = %v", p["model"])
		}
		wd, _ := os.Getwd()
		if p["cwd"] != wd {
			t.Fatalf("cwd = %v, want %v", p["cwd"], wd)
		}
	})

	t.Run("spools without URL/token", func(t *testing.T) {
		r := newRun(t, nil)
		delete(r.env, "BATON_URL")
		delete(r.env, "BATON_TOKEN")
		if baton(t, r.env, []string{"heartbeat"}, "").Code != 0 {
			t.Fatal("non-zero exit")
		}
		if n := len(r.outbox(t)); n != 1 {
			t.Fatalf("outbox has %d lines", n)
		}
	})

	t.Run("plan from stdin, bare array, and --file", func(t *testing.T) {
		steps := []any{map[string]any{"id": "s1", "title": "T", "acceptance": "A"}}
		stepsJSON, _ := json.Marshal(steps)

		a := newRun(t, nil)
		in, _ := json.Marshal(map[string]any{"steps": steps, "reason": "r"})
		if baton(t, a.env, []string{"plan"}, string(in)).Code != 0 {
			t.Fatal("stdin object: non-zero exit")
		}
		if got := a.outbox(t)[0]["payload"]; !reflect.DeepEqual(got, map[string]any{"steps": steps, "reason": "r"}) {
			t.Fatalf("payload = %v", got)
		}

		b := newRun(t, nil)
		if baton(t, b.env, []string{"plan"}, string(stepsJSON)).Code != 0 {
			t.Fatal("bare array: non-zero exit")
		}
		if got := b.outbox(t)[0]["payload"]; !reflect.DeepEqual(got, map[string]any{"steps": steps}) {
			t.Fatalf("payload = %v", got)
		}

		c := newRun(t, nil)
		f := filepath.Join(t.TempDir(), "plan.json")
		if err := os.WriteFile(f, stepsJSON, 0o644); err != nil {
			t.Fatal(err)
		}
		if baton(t, c.env, []string{"plan", "--file", f}, "").Code != 0 {
			t.Fatal("--file: non-zero exit")
		}
		if got := c.outbox(t)[0]["payload"]; !reflect.DeepEqual(got, map[string]any{"steps": steps}) {
			t.Fatalf("payload = %v", got)
		}
	})
}

func TestCLIUsageErrors(t *testing.T) {
	bad := []struct {
		name  string
		args  []string
		input string
	}{
		{"bad percent", []string{"progress", "101", "x"}, ""},
		{"non-numeric percent", []string{"progress", "abc", "x"}, ""},
		{"bad status", []string{"done", "--status", "great", "--summary", "x"}, ""},
		{"missing summary", []string{"step", "s1", "done"}, ""},
		{"bad step action", []string{"step", "s1", "jump"}, ""},
		{"bad artifact kind", []string{"artifact", "zip", "x"}, ""},
		{"bad plan json", []string{"plan"}, "{nope"},
		{"bad plan shape", []string{"plan"}, `{"steps":[]}`},
		{"unknown command", []string{"frobnicate"}, ""},
		{"unknown flag", []string{"hello", "--wat"}, ""},
		{"no command", []string{}, ""},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			r := newRun(t, nil)
			res := baton(t, r.env, c.args, c.input)
			if res.Code != 2 {
				t.Fatalf("exit %d, want 2 (out=%q err=%q)", res.Code, res.Out, res.Err)
			}
			if !strings.HasPrefix(res.Err, "baton: ") {
				t.Fatalf("stderr = %q", res.Err)
			}
			if n := len(r.outbox(t)); n != 0 {
				t.Fatalf("outbox has %d lines", n)
			}
		})
	}

	t.Run("missing env", func(t *testing.T) {
		r := newRun(t, nil)
		delete(r.env, "BATON_RUN_ID")
		res := baton(t, r.env, []string{"hello"}, "")
		if res.Code != 2 || !strings.HasPrefix(res.Err, "baton: not inside a Baton run") {
			t.Fatalf("%+v", res)
		}
	})

	t.Run("help works without env", func(t *testing.T) {
		requireBin(t)
		res := baton(t, map[string]string{}, []string{"help"}, "")
		if res.Code != 0 || !strings.HasPrefix(res.Out, "usage: baton") || res.Err != "" {
			t.Fatalf("%+v", res)
		}
	})
}

func TestCLIInbox(t *testing.T) {
	t.Run("prints unread, marks read, then empty", func(t *testing.T) {
		r := newRun(t, nil)
		putInbox(t, r.dir, 3, "answer", map[string]any{"qid": "q_ab12cd", "text": "use postgres"})
		putInbox(t, r.dir, 4, "message", map[string]any{"from": "conductor", "text": "rebase onto main"})
		putInbox(t, r.dir, 5, "plan_update", map[string]any{"steps": []any{
			map[string]any{"id": "s1", "title": "One", "acceptance": "a"},
			map[string]any{"id": "s2", "title": "Two", "acceptance": "a"},
		}})
		putInbox(t, r.dir, 6, "stop", map[string]any{"reason": "enough"})
		for _, f := range []string{"000007.json.tmp", "junk.json"} {
			if err := os.WriteFile(filepath.Join(r.dir, "inbox", f), []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		res := baton(t, r.env, []string{"inbox"}, "")
		want := strings.Join([]string{
			"[3] answer q_ab12cd: use postgres",
			"[4] message (conductor): rebase onto main",
			"[5] plan_update: s1 One; s2 Two",
			"[6] stop: enough",
		}, "\n")
		if res.Code != 0 || strings.ReplaceAll(res.Out, "\r\n", "\n") != want {
			t.Fatalf("inbox = %+v\nwant %q", res, want)
		}
		if got := readLines(t, filepath.Join(r.dir, "inbox", ".read")); !reflect.DeepEqual(got, []string{"3", "4", "5", "6"}) {
			t.Fatalf(".read = %v", got)
		}
		if out := baton(t, r.env, []string{"inbox"}, "").Out; out != "inbox empty" {
			t.Fatalf("second inbox = %q", out)
		}
		putInbox(t, r.dir, 8, "resume", map[string]any{})
		if out := baton(t, r.env, []string{"inbox"}, "").Out; out != "[8] resume" {
			t.Fatalf("third inbox = %q", out)
		}
	})

	t.Run("empty when no inbox dir", func(t *testing.T) {
		r := newRun(t, nil)
		if err := os.MkdirAll(r.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if out := baton(t, r.env, []string{"inbox"}, "").Out; out != "inbox empty" {
			t.Fatalf("inbox = %q", out)
		}
	})
}

func TestCLIAskBlocking(t *testing.T) {
	t.Run("times out with NO_ANSWER", func(t *testing.T) {
		r := newRun(t, map[string]string{"BATON_POLL_INTERVAL_S": "0.1"})
		res := baton(t, r.env, []string{"ask", "q?", "--blocking", "--timeout", "0.5"}, "")
		if res.Code != 0 || res.Out != "NO_ANSWER" {
			t.Fatalf("%+v", res)
		}
		if r.outbox(t)[0]["payload"].(map[string]any)["blocking"] != true {
			t.Fatal("question not blocking")
		}
	})

	t.Run("finds its answer, leaves other messages unread", func(t *testing.T) {
		r := newRun(t, map[string]string{"BATON_POLL_INTERVAL_S": "0.1"})
		done := batonAsync(r.env, []string{"ask", "which db?", "--blocking", "--timeout", "15"}, 25*time.Second)
		// the question must be spooled before we can read its qid
		qid := until(t, 10*time.Second, "spooled question", func() (string, bool) {
			ob := r.outbox(t)
			if len(ob) == 0 {
				return "", false
			}
			q, _ := ob[0]["payload"].(map[string]any)["qid"].(string)
			return q, q != ""
		})
		putInbox(t, r.dir, 1, "answer", map[string]any{"qid": "q_other1", "text": "not mine"})
		putInbox(t, r.dir, 2, "message", map[string]any{"from": "human", "text": "hi"})
		putInbox(t, r.dir, 3, "answer", map[string]any{"qid": qid, "text": "postgres"})
		res := waitRes(t, done, 20*time.Second)
		if res.Code != 0 || res.Out != "postgres" {
			t.Fatalf("%+v", res)
		}
		if got := readLines(t, filepath.Join(r.dir, "inbox", ".read")); !reflect.DeepEqual(got, []string{"3"}) {
			t.Fatalf(".read = %v", got)
		}
		out := strings.ReplaceAll(baton(t, r.env, []string{"inbox"}, "").Out, "\r\n", "\n")
		if out != "[1] answer q_other1: not mine\n[2] message (human): hi" {
			t.Fatalf("inbox = %q", out)
		}
	})

	t.Run("sends heartbeats while waiting", func(t *testing.T) {
		r := newRun(t, map[string]string{"BATON_POLL_INTERVAL_S": "0.05", "BATON_HEARTBEAT_INTERVAL_S": "0.2"})
		baton(t, r.env, []string{"ask", "q?", "--blocking", "--timeout", "0.7"}, "")
		n := 0
		for _, m := range r.outbox(t) {
			if m["type"] == "heartbeat" {
				n++
			}
		}
		if n < 1 {
			t.Fatal("no heartbeat spooled while waiting")
		}
	})
}

type seenReq struct {
	Auth string
	Body map[string]any
}

type fakeHub struct {
	mu     sync.Mutex
	status int
	reply  string
	seen   []seenReq
	url    string
}

func (f *fakeHub) set(status int, reply any) {
	b, _ := json.Marshal(reply)
	f.mu.Lock()
	f.status, f.reply = status, string(b)
	f.mu.Unlock()
}

func (f *fakeHub) last() seenReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[len(f.seen)-1]
}

func newFakeHub(t *testing.T) *fakeHub {
	f := &fakeHub{status: 200, reply: "{}"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		var body map[string]any
		if len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
		f.mu.Lock()
		f.seen = append(f.seen, seenReq{req.Header.Get("Authorization"), body})
		status, reply := f.status, f.reply
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func TestCLIFakeHub(t *testing.T) {
	f := newFakeHub(t)

	t.Run("200 prints ok #seq and does not spool", func(t *testing.T) {
		f.set(200, map[string]any{"ok": true, "seq": 7, "duplicate": false})
		r := newRun(t, map[string]string{"BATON_URL": f.url})
		res := baton(t, r.env, []string{"progress", "10", "x"}, "")
		if res.Code != 0 || res.Out != "ok #7" {
			t.Fatalf("%+v", res)
		}
		if n := len(r.outbox(t)); n != 0 {
			t.Fatalf("outbox has %d lines", n)
		}
		last := f.last()
		if last.Auth != "Bearer bt_run_x" || last.Body["type"] != "progress" || last.Body["msg_id"] == nil || last.Body["msg_id"] == "" {
			t.Fatalf("last request = %+v", last)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		f.set(200, map[string]any{"ok": true, "seq": 8, "duplicate": true})
		r := newRun(t, map[string]string{"BATON_URL": f.url})
		if out := baton(t, r.env, []string{"heartbeat"}, "").Out; out != "ok #8 (dup)" {
			t.Fatalf("out = %q", out)
		}
	})

	t.Run("500 spools", func(t *testing.T) {
		f.set(500, map[string]any{"ok": false, "code": "X", "error": "x"})
		r := newRun(t, map[string]string{"BATON_URL": f.url})
		res := baton(t, r.env, []string{"heartbeat"}, "")
		if res.Code != 0 || !strings.HasPrefix(res.Out, "spooled") {
			t.Fatalf("%+v", res)
		}
		if n := len(r.outbox(t)); n != 1 {
			t.Fatalf("outbox has %d lines", n)
		}
	})

	t.Run("410 exits 0 without spooling", func(t *testing.T) {
		f.set(410, map[string]any{"ok": false, "code": "RUN_ENDED", "error": "ended"})
		r := newRun(t, map[string]string{"BATON_URL": f.url})
		res := baton(t, r.env, []string{"heartbeat"}, "")
		if res.Code != 0 || !strings.Contains(res.Err, "rejected: RUN_ENDED") {
			t.Fatalf("%+v", res)
		}
		if n := len(r.outbox(t)); n != 0 {
			t.Fatalf("outbox has %d lines", n)
		}
	})

	t.Run("400 exits 2", func(t *testing.T) {
		f.set(400, map[string]any{"ok": false, "code": "INVALID_PAYLOAD", "error": "bad"})
		r := newRun(t, map[string]string{"BATON_URL": f.url})
		res := baton(t, r.env, []string{"heartbeat"}, "")
		if res.Code != 2 || !strings.Contains(res.Err, "rejected: INVALID_PAYLOAD bad") {
			t.Fatalf("%+v", res)
		}
	})

	t.Run("inbox falls back to the API when the run dir is missing", func(t *testing.T) {
		f.set(200, map[string]any{"ok": true, "messages": []any{
			map[string]any{"v": 1, "n": 2, "ts": "t", "type": "message", "payload": map[string]any{"from": "human", "text": "yo"}},
		}})
		r := newRun(t, map[string]string{"BATON_URL": f.url})
		if out := baton(t, r.env, []string{"inbox"}, "").Out; out != "[2] message (human): yo" {
			t.Fatalf("out = %q", out)
		}
	})
}
