package api_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/api"
	"github.com/whosabhiraj/baton/internal/bus"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/store"
)

const adminTok = "bt_adm_test"

type env struct {
	t        *testing.T
	srv      *httptest.Server
	st       *store.Store
	br       *bus.Broker
	shutdown atomic.Int32
	done     chan struct{}
}

func newEnv(t *testing.T, mod func(*api.Deps)) *env {
	t.Helper()
	e := &env{t: t, done: make(chan struct{})}
	e.br = bus.New(100, 8)
	var err error
	e.st, err = store.Open(t.TempDir(), store.Options{Publish: func(ev proto.StoredEvent) { e.br.Publish(ev) }})
	if err != nil {
		t.Fatal(err)
	}
	d := api.Deps{Store: e.st, Bus: e.br, AdminToken: adminTok, Instance: "inst-1", Dev: true, Done: e.done,
		Shutdown: func() { e.shutdown.Add(1) }, KeepAlive: 50 * time.Millisecond}
	if mod != nil {
		mod(&d)
	}
	e.srv = httptest.NewServer(api.New(d))
	t.Cleanup(func() {
		close(e.done)
		e.br.Close()
		e.srv.Close()
		e.st.Close()
	})
	return e
}

func (e *env) do(method, path, tok, body string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		e.t.Fatalf("%s %s: non-JSON response %q", method, path, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		e.t.Fatalf("content-type %q", ct)
	}
	if m["ok"] == false {
		if err := proto.ValidateJSON("api.error", m); err != nil {
			e.t.Fatalf("error body invalid: %v (%s)", err, b)
		}
	}
	return resp.StatusCode, m
}

func (e *env) expectErr(method, path, tok, body string, status int, code string) {
	e.t.Helper()
	st, m := e.do(method, path, tok, body)
	if st != status || m["code"] != code {
		e.t.Fatalf("%s %s: got %d %v, want %d %s", method, path, st, m, status, code)
	}
}

func (e *env) newRun(id string) (token string) {
	e.t.Helper()
	_, tok, err := e.st.Create(store.CreateOpts{RunID: id, AgentID: "a-" + id,
		Plan: []proto.PlanStep{{ID: "s1", Title: "t", Acceptance: "a"}}})
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func hbBody(run string) string {
	return `{"v":1,"run_id":"` + run + `","agent_id":"a-` + run + `","type":"heartbeat","msg_id":"` + run + `-` + time.Now().Format("150405.000000000") + `"}`
}

func TestHealth(t *testing.T) {
	e := newEnv(t, nil)
	st, m := e.do("GET", "/api/health", "", "")
	if st != 200 || m["instance"] != "inst-1" {
		t.Fatal(st, m)
	}
	if err := proto.ValidateJSON("api.health", m); err != nil {
		t.Fatal(err)
	}
}

func TestIngestAuthMatrix(t *testing.T) {
	e := newEnv(t, nil)
	tokA := e.newRun("rA")
	tokB := e.newRun("rB")
	bodyA := `{"v":1,"run_id":"rA","agent_id":"a-rA","type":"heartbeat","msg_id":"m1"}`
	e.expectErr("POST", "/api/ingest", "", bodyA, 401, "UNAUTHORIZED")
	e.expectErr("POST", "/api/ingest", "bt_run_wrong", bodyA, 401, "UNAUTHORIZED")
	e.expectErr("POST", "/api/ingest", adminTok, bodyA, 401, "UNAUTHORIZED")
	e.expectErr("POST", "/api/ingest", tokB, bodyA, 403, "RUN_MISMATCH")
	e.expectErr("POST", "/api/ingest", tokA, `{"v":1,"run_id":"rA","agent_id":"nope","type":"heartbeat"}`, 403, "AGENT_MISMATCH")
	e.expectErr("POST", "/api/ingest", tokA, `not json`, 400, "INVALID_ENVELOPE")
	e.expectErr("POST", "/api/ingest", tokA, `{"v":1,"run_id":"rA","agent_id":"a-rA","type":"exit","payload":{}}`, 400, "HUB_ONLY_TYPE")
	e.expectErr("POST", "/api/ingest", tokA, `{"v":1,"run_id":"rA","agent_id":"a-rA","type":"progress","payload":{"percent":500,"note":"x"}}`, 400, "INVALID_PAYLOAD")

	st, m := e.do("POST", "/api/ingest", tokA, bodyA)
	if st != 200 || m["seq"] != json.Number("1") || m["duplicate"] != false {
		t.Fatal(st, m)
	}
	if err := proto.ValidateJSON("api.ingest-response", m); err != nil {
		t.Fatal(err)
	}
	st, m = e.do("POST", "/api/ingest", tokA, bodyA)
	if st != 200 || m["duplicate"] != true || m["seq"] != json.Number("1") {
		t.Fatal(st, m)
	}
	// done revokes the token: further ingest = 410, reads = 401
	done := `{"v":1,"run_id":"rA","agent_id":"a-rA","type":"done","payload":{"status":"success","summary":"s"},"msg_id":"d"}`
	if st, _ := e.do("POST", "/api/ingest", tokA, done); st != 200 {
		t.Fatal(st)
	}
	e.expectErr("POST", "/api/ingest", tokA, `{"v":1,"run_id":"rA","agent_id":"a-rA","type":"heartbeat","msg_id":"late"}`, 410, "RUN_ENDED")
	e.expectErr("GET", "/api/runs/rA/inbox", tokA, "", 401, "UNAUTHORIZED")
	e.expectErr("GET", "/api/runs/rA/events", tokA, "", 401, "UNAUTHORIZED")
}

func TestReadAuthAndShapes(t *testing.T) {
	e := newEnv(t, nil)
	tokA := e.newRun("rA")
	tokB := e.newRun("rB")
	e.do("POST", "/api/ingest", tokA, `{"v":1,"run_id":"rA","agent_id":"a-rA","type":"heartbeat","msg_id":"m1"}`)
	if st, m := e.do("POST", "/api/runs/rA/message", adminTok, `{"text":"hi"}`); st != 200 || m["n"] != json.Number("1") {
		t.Fatal(st, m)
	}
	for _, p := range []string{"/api/runs/rA/inbox", "/api/runs/rA/events"} {
		e.expectErr("GET", p, "", "", 401, "UNAUTHORIZED")
		e.expectErr("GET", p, "bogus", "", 401, "UNAUTHORIZED")
		e.expectErr("GET", p, tokB, "", 403, "RUN_MISMATCH")
		for _, tok := range []string{tokA, adminTok} {
			st, m := e.do("GET", p, tok, "")
			if st != 200 {
				t.Fatal(p, st, m)
			}
			schema := "api.inbox"
			if strings.HasSuffix(p, "events") {
				schema = "api.events"
			}
			if err := proto.ValidateJSON(schema, m); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
		}
	}
	e.expectErr("GET", "/api/runs/nope/inbox", adminTok, "", 404, "NOT_FOUND")
	_, m := e.do("GET", "/api/runs/rA/inbox?after=1", tokA, "")
	if len(m["messages"].([]any)) != 0 {
		t.Fatal("after=1 should be empty")
	}
	_, m = e.do("GET", "/api/runs/rA/events?since=1", adminTok, "")
	if len(m["events"].([]any)) != 1 { // to_agent
		t.Fatalf("events since 1: %v", m["events"])
	}
	e.expectErr("GET", "/api/runs/rA/events?since=x", adminTok, "", 400, "INVALID_PAYLOAD")

	// state: admin only
	e.expectErr("GET", "/api/state", "", "", 401, "UNAUTHORIZED")
	e.expectErr("GET", "/api/state", tokA, "", 401, "UNAUTHORIZED")
	st, m := e.do("GET", "/api/state", adminTok, "")
	if st != 200 {
		t.Fatal(st)
	}
	if err := proto.ValidateJSON("api.state", m); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mustJSON(m), "token_sha256") {
		t.Fatal("state leaks token hash")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestAdminDeliverRoutes(t *testing.T) {
	e := newEnv(t, nil)
	tok := e.newRun("rA")
	e.expectErr("POST", "/api/runs/rA/message", tok, `{"text":"x"}`, 401, "UNAUTHORIZED")
	e.expectErr("POST", "/api/runs/rA/message", "", `{"text":"x"}`, 401, "UNAUTHORIZED")
	e.expectErr("POST", "/api/runs/nope/message", adminTok, `{"text":"x"}`, 404, "NOT_FOUND")
	e.expectErr("POST", "/api/runs/rA/message", adminTok, `{"text":""}`, 400, "INVALID_PAYLOAD")
	e.expectErr("POST", "/api/runs/rA/message", adminTok, `[1]`, 400, "INVALID_PAYLOAD")
	for i, c := range []struct{ path, body string }{
		{"message", `{"text":"hi","from":"conductor"}`},
		{"answer", `{"qid":"q_1","text":"yes"}`},
		{"plan_update", `{"steps":[{"id":"s1","title":"t","acceptance":"a"}]}`},
		{"pause", ``},
		{"resume", `{"reason":"go"}`},
		{"stop", `{}`},
	} {
		st, m := e.do("POST", "/api/runs/rA/"+c.path, adminTok, c.body)
		if st != 200 || m["n"] != json.Number(string(rune('1'+i))) {
			t.Fatalf("%s: %d %v", c.path, st, m)
		}
	}
	_, m := e.do("GET", "/api/runs/rA/inbox", tok, "")
	msgs := m["messages"].([]any)
	if len(msgs) != 6 || msgs[0].(map[string]any)["payload"].(map[string]any)["from"] != "conductor" {
		t.Fatalf("%v", msgs)
	}
	e.expectErr("GET", "/api/runs/rA/message", adminTok, "", 405, "METHOD_NOT_ALLOWED")
}

func TestRoutesMisc(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.MaxBody = 256 })
	e.expectErr("GET", "/api/nope", adminTok, "", 404, "NOT_FOUND")
	e.expectErr("POST", "/api/health", "", "", 405, "METHOD_NOT_ALLOWED")
	tok := e.newRun("rA")
	e.expectErr("POST", "/api/ingest", tok, strings.Repeat("x", 1000), 413, "PAYLOAD_TOO_LARGE")
	st, _ := e.do("POST", "/api/hub/shutdown", adminTok, "")
	if st != 200 {
		t.Fatal(st)
	}
	waitFor(t, func() bool { return e.shutdown.Load() == 1 })
	e.expectErr("POST", "/api/hub/shutdown", "", "", 401, "UNAUTHORIZED")
}

func TestPanicRecovered(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) {
		d.Dashboard = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	})
	e.expectErr("GET", "/", "", "", 500, "INTERNAL")
	if st, _ := e.do("GET", "/api/health", "", ""); st != 200 {
		t.Fatal("server must keep serving")
	}
}

func TestDashboardFallback(t *testing.T) {
	e := newEnv(t, nil)
	resp, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Dashboard not built") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestHostGuard(t *testing.T) {
	e := newEnv(t, nil)
	get := func(host string) int {
		req, _ := http.NewRequest("GET", e.srv.URL+"/api/health", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, h := range []string{"evil.example.com", "evil.example.com:80", "127.0.0.1.evil.com", "10.0.0.1:7777", "localhost.evil.com:7777"} {
		if get(h) != 403 {
			t.Fatalf("host %q should be refused", h)
		}
	}
	for _, h := range []string{"127.0.0.1:7777", "localhost:7777", "[::1]:7777", "LOCALHOST:7777"} {
		if get(h) != 200 {
			t.Fatalf("host %q should be allowed", h)
		}
	}
	// with a known listener port the port must match too
	e2 := newEnv(t, func(d *api.Deps) { d.Port = 4321 })
	req, _ := http.NewRequest("GET", e2.srv.URL+"/api/health", nil)
	req.Host = "127.0.0.1:9999"
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("wrong port must be refused")
	}
	req.Host = "127.0.0.1:4321"
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("right port must pass")
	}
}

func TestDevRuns(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.Dev = false })
	e.expectErr("POST", "/api/dev/runs", adminTok, `{}`, 404, "NOT_FOUND")

	e = newEnv(t, nil)
	e.expectErr("POST", "/api/dev/runs", "", `{}`, 401, "UNAUTHORIZED")
	st, m := e.do("POST", "/api/dev/runs", adminTok, `{"plan":[{"id":"s1","title":"T","acceptance":"A"}]}`)
	if st != 200 {
		t.Fatal(st, m)
	}
	envm := m["env"].(map[string]any)
	for _, k := range []string{"BATON_URL", "BATON_TOKEN", "BATON_RUN_ID", "BATON_AGENT_ID", "BATON_RUN_DIR", "BATON_BRIEF"} {
		if s, _ := envm[k].(string); s == "" {
			t.Fatalf("env %s missing", k)
		}
	}
	if envm["BATON_URL"] != e.srv.URL || envm["BATON_TOKEN"] != m["token"] || !strings.HasPrefix(m["agent_id"].(string), "gn-dev-") {
		t.Fatalf("%v", m)
	}
	brief, err := os.ReadFile(envm["BATON_BRIEF"].(string))
	if err != nil || !strings.Contains(string(brief), "**s1** T") {
		t.Fatalf("brief: %v %q", err, brief)
	}
	// the returned token works
	body := `{"v":1,"run_id":"` + m["run_id"].(string) + `","agent_id":"` + m["agent_id"].(string) + `","type":"hello","payload":{"brief_ack":true},"msg_id":"h"}`
	if st, _ := e.do("POST", "/api/ingest", m["token"].(string), body); st != 200 {
		t.Fatal(st)
	}
	e.expectErr("POST", "/api/dev/runs", adminTok, `{"plan":[{"id":"s1"}]}`, 400, "INVALID_PAYLOAD")
}

// ---- SSE ----

type frame struct{ id, event, data string }

type sseReader struct {
	t  *testing.T
	ch chan frame
	r  *http.Response
}

func openSSE(t *testing.T, url, tok string, hdr map[string]string) *sseReader {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("stream status %d: %s", resp.StatusCode, b)
	}
	s := &sseReader{t: t, ch: make(chan frame, 1000), r: resp}
	go func() {
		defer close(s.ch)
		br := bufio.NewReader(resp.Body)
		var f frame
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if f != (frame{}) {
					s.ch <- f
				}
				f = frame{}
			case strings.HasPrefix(line, ":"):
				s.ch <- frame{event: "comment", data: line}
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				f.data = line[6:]
			}
		}
	}()
	t.Cleanup(func() { resp.Body.Close() })
	return s
}

func (s *sseReader) next(skipComments bool) (frame, bool) {
	s.t.Helper()
	for {
		select {
		case f, ok := <-s.ch:
			if !ok {
				return frame{}, false
			}
			if skipComments && f.event == "comment" {
				continue
			}
			return f, true
		case <-time.After(5 * time.Second):
			s.t.Fatal("timeout waiting for SSE frame")
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestStreamAuthAndHello(t *testing.T) {
	e := newEnv(t, nil)
	tok := e.newRun("rA")
	e.expectErr("GET", "/api/stream", "", "", 401, "UNAUTHORIZED")
	e.expectErr("GET", "/api/stream", tok, "", 401, "UNAUTHORIZED")
	e.expectErr("GET", "/api/stream?token=bt_run_x", "", "", 401, "UNAUTHORIZED")
	// ?token= works only on the stream
	e.expectErr("GET", "/api/state?token="+adminTok, "", "", 401, "UNAUTHORIZED")

	s := openSSE(t, e.srv.URL+"/api/stream?token="+adminTok, "", nil)
	f, _ := s.next(true)
	if f.event != "hello" || f.data != `{"instance":"inst-1"}` {
		t.Fatalf("first frame %+v", f)
	}
	if got := s.r.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatal(got)
	}
	e.do("POST", "/api/ingest", tok, `{"v":1,"run_id":"rA","agent_id":"a-rA","type":"heartbeat","msg_id":"m1"}`)
	f, _ = s.next(true)
	if f.event != "baton" || f.id != "1" {
		t.Fatalf("%+v", f)
	}
	var ev proto.StoredEvent
	if err := json.Unmarshal([]byte(f.data), &ev); err != nil || ev.Seq != 1 || ev.Type != "heartbeat" {
		t.Fatalf("%v %+v", err, ev)
	}
	// keepalive comment arrives while idle
	for {
		f, ok := s.next(false)
		if !ok {
			t.Fatal("stream closed")
		}
		if f.event == "comment" {
			break
		}
	}
}

func TestStreamReplayAndReset(t *testing.T) {
	e := newEnv(t, nil)
	tok := e.newRun("rA")
	for i := 0; i < 5; i++ {
		e.do("POST", "/api/ingest", tok, `{"v":1,"run_id":"rA","agent_id":"a-rA","type":"heartbeat"}`)
	}
	// replay after id 3
	s := openSSE(t, e.srv.URL+"/api/stream", adminTok, map[string]string{"Last-Event-ID": "3"})
	f, _ := s.next(true)
	if f.event != "hello" {
		t.Fatalf("%+v", f)
	}
	for _, want := range []string{"4", "5"} {
		f, _ = s.next(true)
		if f.event != "baton" || f.id != want {
			t.Fatalf("want id %s got %+v", want, f)
		}
	}
	// future id -> reset after hello, then live events continue
	s2 := openSSE(t, e.srv.URL+"/api/stream", adminTok, map[string]string{"Last-Event-ID": "999"})
	f, _ = s2.next(true)
	f2, _ := s2.next(true)
	if f.event != "hello" || f2.event != "reset" {
		t.Fatalf("%+v %+v", f, f2)
	}
	e.do("POST", "/api/ingest", tok, `{"v":1,"run_id":"rA","agent_id":"a-rA","type":"heartbeat"}`)
	if f, _ = s2.next(true); f.event != "baton" || f.id != "6" {
		t.Fatalf("%+v", f)
	}
	// too old: ring is 100 in this env; publish 150 more
	for i := 0; i < 150; i++ {
		e.br.Publish(proto.StoredEvent{Seq: int64(100 + i), Type: "heartbeat"})
	}
	s3 := openSSE(t, e.srv.URL+"/api/stream", adminTok, map[string]string{"Last-Event-ID": "2"})
	s3.next(true)
	if f, _ = s3.next(true); f.event != "reset" {
		t.Fatalf("old id should reset: %+v", f)
	}
}

func TestStreamSlowClientGetsReset(t *testing.T) {
	e := newEnv(t, nil) // sub buffer 8
	s := openSSE(t, e.srv.URL+"/api/stream", adminTok, nil)
	s.next(true) // hello
	// flood far past the buffer without reading
	for i := 0; i < 5000; i++ {
		e.br.Publish(proto.StoredEvent{Seq: int64(i + 1), Type: "heartbeat", Payload: map[string]any{"pad": strings.Repeat("x", 2000)}})
	}
	sawReset := false
	for {
		f, ok := s.next(true)
		if !ok {
			break
		}
		if f.event == "reset" {
			sawReset = true
		}
	}
	if !sawReset {
		t.Fatal("dropped subscriber should receive reset before the stream closes")
	}
}

func TestStreamEndsOnShutdown(t *testing.T) {
	e := newEnv(t, nil)
	s := openSSE(t, e.srv.URL+"/api/stream", adminTok, nil)
	s.next(true)
	e.br.Close()
	if _, ok := s.next(true); ok {
		t.Fatal("stream should close")
	}
}
