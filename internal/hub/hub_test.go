package hub_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
)

func start(t *testing.T, o hub.Options) *hub.Hub {
	t.Helper()
	if o.SpoolPoll == 0 {
		o.SpoolPoll = 30 * time.Millisecond
	}
	h, err := hub.Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(context.Background()) })
	return h
}

func call(t *testing.T, h *hub.Hub, method, path, tok, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	_ = dec.Decode(&m)
	return resp.StatusCode, m
}

func devRun(t *testing.T, h *hub.Hub, body string) map[string]any {
	t.Helper()
	st, m := call(t, h, "POST", "/api/dev/runs", h.AdminToken, body)
	if st != 200 {
		t.Fatalf("dev run: %d %v", st, m)
	}
	return m
}

func ingest(t *testing.T, h *hub.Hub, run map[string]any, typ, msgID, payload string) (int, map[string]any) {
	t.Helper()
	if payload == "" {
		payload = "{}"
	}
	body := fmt.Sprintf(`{"v":1,"run_id":%q,"agent_id":%q,"type":%q,"msg_id":%q,"payload":%s}`,
		run["run_id"], run["agent_id"], typ, msgID, payload)
	return call(t, h, "POST", "/api/ingest", run["token"].(string), body)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func status(t *testing.T, h *hub.Hub, runID string) string {
	t.Helper()
	_, m := call(t, h, "GET", "/api/state", h.AdminToken, "")
	for _, r := range m["runs"].([]any) {
		rm := r.(map[string]any)
		if rm["run_id"] == runID {
			return rm["status"].(string)
		}
	}
	return ""
}

func TestStartServesAndCloses(t *testing.T) {
	dir := t.TempDir()
	h, err := hub.Start(context.Background(), hub.Options{DataDir: dir, Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	if h.Port == 0 || !strings.HasPrefix(h.URL, "http://127.0.0.1:") || h.DataDir == "" || !strings.HasPrefix(h.AdminToken, "bt_adm_") {
		t.Fatalf("%+v", h)
	}
	st, m := call(t, h, "GET", "/api/health", "", "")
	if st != 200 || m["instance"] == "" {
		t.Fatal(st, m)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(context.Background()); err != nil { // idempotent
		t.Fatal(err)
	}
	select {
	case <-h.Done():
	default:
		t.Fatal("Done not closed")
	}
	if _, err := http.Get(h.URL + "/api/health"); err == nil {
		t.Fatal("listener should be closed")
	}
}

func TestSecondStartAlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	h := start(t, hub.Options{DataDir: dir})
	_, err := hub.Start(context.Background(), hub.Options{DataDir: dir})
	if !errors.Is(err, hub.ErrAlreadyRunning) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatal(err)
	}
	h.Close(context.Background())
	// after Close the lock is free again
	h2, err := hub.Start(context.Background(), hub.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	h2.Close(context.Background())
}

func TestNonLoopbackRefused(t *testing.T) {
	_, err := hub.Start(context.Background(), hub.Options{DataDir: t.TempDir(), Bind: "0.0.0.0"})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err = %v", err)
	}
}

func TestAdminTokenPersists(t *testing.T) {
	dir := t.TempDir()
	h := start(t, hub.Options{DataDir: dir})
	tok := h.AdminToken
	b, err := os.ReadFile(filepath.Join(dir, hub.AdminTokenFile))
	if err != nil || strings.TrimSpace(string(b)) != tok {
		t.Fatalf("token file: %v %q", err, b)
	}
	h.Close(context.Background())
	h2 := start(t, hub.Options{DataDir: dir})
	if h2.AdminToken != tok {
		t.Fatal("admin token must persist across restarts")
	}
	if st, _ := call(t, h2, "GET", "/api/state", tok, ""); st != 200 {
		t.Fatal(st)
	}
	h2.Close(context.Background())
	// explicit token wins and is not written
	h3 := start(t, hub.Options{DataDir: dir, AdminToken: "bt_adm_explicit"})
	if h3.AdminToken != "bt_adm_explicit" {
		t.Fatal("explicit token ignored")
	}
	b, _ = os.ReadFile(filepath.Join(dir, hub.AdminTokenFile))
	if strings.TrimSpace(string(b)) != tok {
		t.Fatal("explicit token must not overwrite the file")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestPortFallbackAndRemember(t *testing.T) {
	// occupy a port
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	busy := blocker.Addr().(*net.TCPAddr).Port

	// no fallback: error
	if _, err := hub.Start(context.Background(), hub.Options{DataDir: t.TempDir(), Port: busy}); err == nil {
		t.Fatal("busy port without fallback must fail")
	}
	dir := t.TempDir()
	h := start(t, hub.Options{DataDir: dir, Port: busy, PortFallback: true})
	if h.Port == busy || h.Port < busy || h.Port > busy+20 {
		t.Fatalf("fallback port %d (busy %d)", h.Port, busy)
	}
	if st, _ := call(t, h, "GET", "/api/health", "", ""); st != 200 {
		t.Fatal("not serving on fallback port")
	}
	b, _ := os.ReadFile(filepath.Join(dir, hub.PortFile))
	if strings.TrimSpace(string(b)) != fmt.Sprint(h.Port) {
		t.Fatalf("remembered %q, want %d", b, h.Port)
	}
	chosen := h.Port
	h.Close(context.Background())
	// next start prefers the remembered port even though the configured one is still busy
	h2 := start(t, hub.Options{DataDir: dir, Port: busy, PortFallback: true})
	if h2.Port != chosen {
		t.Fatalf("restart port %d, want remembered %d", h2.Port, chosen)
	}
	h2.Close(context.Background())
	// once the configured port is free and the remembered one is taken, it falls back sensibly
	hold, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", chosen))
	if err == nil {
		defer hold.Close()
		blocker.Close()
		h3 := start(t, hub.Options{DataDir: dir, Port: busy, PortFallback: true})
		if h3.Port == chosen {
			t.Fatal("port should not be reused while held")
		}
	}
}

func TestRestartSeqContinuesNoDuplicates(t *testing.T) {
	dir := t.TempDir()
	h := start(t, hub.Options{DataDir: dir, Dev: true})
	run := devRun(t, h, `{"plan":[{"id":"s1","title":"T","acceptance":"A"}]}`)
	ingest(t, h, run, "hello", "m1", `{"brief_ack":true}`)
	ingest(t, h, run, "heartbeat", "m2", "")
	runDir := run["run_dir"].(string)
	// spooled message while the hub is down, no msg_id
	h.Close(context.Background())
	f, _ := os.OpenFile(filepath.Join(runDir, paths.OutboxFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	fmt.Fprintf(f, `{"v":1,"run_id":%q,"agent_id":%q,"type":"progress","payload":{"percent":3,"note":"n"}}`+"\n", run["run_id"], run["agent_id"])
	fmt.Fprintf(f, `{"v":1,"run_id":%q,"agent_id":%q,"type":"heartbeat","msg_id":"m2"}`+"\n", run["run_id"], run["agent_id"]) // already stored via HTTP
	f.Close()

	h2 := start(t, hub.Options{DataDir: dir, Dev: true})
	// outbox ingested before Start returned
	st, m := call(t, h2, "GET", "/api/runs/"+run["run_id"].(string)+"/events", h2.AdminToken, "")
	if st != 200 {
		t.Fatal(st)
	}
	evs := m["events"].([]any)
	if len(evs) != 3 {
		t.Fatalf("events after recovery: %d %v", len(evs), evs)
	}
	last := evs[2].(map[string]any)
	if last["src"] != "spool" || last["type"] != "progress" || last["msg_id"] != fmt.Sprintf("spool@0") {
		t.Fatalf("%v", last)
	}
	// the old run token still works after restart, seq continues
	st, m = ingest(t, h2, run, "heartbeat", "m3", "")
	if st != 200 || m["seq"] != json.Number("4") {
		t.Fatal(st, m)
	}
	h2.Close(context.Background())

	h3 := start(t, hub.Options{DataDir: dir, Dev: true})
	_, m = call(t, h3, "GET", "/api/runs/"+run["run_id"].(string)+"/events", h3.AdminToken, "")
	seen := map[string]bool{}
	for i, e := range m["events"].([]any) {
		em := e.(map[string]any)
		if em["seq"] != json.Number(fmt.Sprint(i+1)) {
			t.Fatalf("event %d has seq %v", i, em["seq"])
		}
		id := em["msg_id"].(string)
		if seen[id] {
			t.Fatalf("duplicate msg_id %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 4 {
		t.Fatalf("want 4 events, got %d", len(seen))
	}
}

func TestSpoolTailedWhileRunning(t *testing.T) {
	h := start(t, hub.Options{DataDir: t.TempDir(), Dev: true})
	run := devRun(t, h, `{}`)
	p := filepath.Join(run["run_dir"].(string), paths.OutboxFile)
	body := fmt.Sprintf(`{"v":1,"run_id":%q,"agent_id":%q,"type":"heartbeat","msg_id":"s1"}`+"\n", run["run_id"], run["agent_id"])
	os.WriteFile(p, []byte(body), 0o644)
	waitFor(t, "spooled event", func() bool {
		_, m := call(t, h, "GET", "/api/runs/"+run["run_id"].(string)+"/events", h.AdminToken, "")
		return len(m["events"].([]any)) == 1
	})
}

func TestStalledEmitAndClear(t *testing.T) {
	h := start(t, hub.Options{DataDir: t.TempDir(), Dev: true, HeartbeatTimeout: 200 * time.Millisecond})
	run := devRun(t, h, `{}`)
	rid := run["run_id"].(string)
	ingest(t, h, run, "hello", "m1", `{"brief_ack":true}`)
	waitFor(t, "stalled status", func() bool { return status(t, h, rid) == "stalled" })

	_, m := call(t, h, "GET", "/api/runs/"+rid+"/events?since=1", h.AdminToken, "")
	evs := m["events"].([]any)
	if len(evs) != 1 {
		t.Fatalf("expected exactly one stalled event, got %v", evs)
	}
	ev := evs[0].(map[string]any)
	if ev["type"] != "stalled" || ev["src"] != "hub" {
		t.Fatalf("%v", ev)
	}
	p := ev["payload"].(map[string]any)
	if p["last_seen"] == nil || fmt.Sprint(p["timeout_s"]) != "0.2" {
		t.Fatalf("%v", p)
	}
	if err := proto.ValidateJSON("api.events", m); err != nil {
		t.Fatal(err)
	}
	time.Sleep(450 * time.Millisecond) // still stalled, never ended, no second event
	if status(t, h, rid) != "stalled" {
		t.Fatal("stall must not clear or end the run by itself")
	}
	_, m = call(t, h, "GET", "/api/runs/"+rid+"/events?since=2", h.AdminToken, "")
	if len(m["events"].([]any)) != 0 {
		t.Fatalf("repeated stalled events: %v", m["events"])
	}
	// next agent event clears
	ingest(t, h, run, "heartbeat", "m2", "")
	if s := status(t, h, rid); s != "running" {
		t.Fatalf("status after agent event = %s", s)
	}
	// and the detector can stall it again later
	waitFor(t, "second stall", func() bool { return status(t, h, rid) == "stalled" })
}

func TestStalledNotForEndedRuns(t *testing.T) {
	h := start(t, hub.Options{DataDir: t.TempDir(), Dev: true, HeartbeatTimeout: 100 * time.Millisecond})
	run := devRun(t, h, `{}`)
	ingest(t, h, run, "done", "d", `{"status":"success","summary":"ok"}`)
	time.Sleep(400 * time.Millisecond)
	if s := status(t, h, run["run_id"].(string)); s != "done" {
		t.Fatalf("status = %s", s)
	}
}

func TestShutdownRoute(t *testing.T) {
	h := start(t, hub.Options{DataDir: t.TempDir()})
	// open a stream to prove shutdown is not blocked by SSE
	req, _ := http.NewRequest("GET", h.URL+"/api/stream", nil)
	req.Header.Set("Authorization", "Bearer "+h.AdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	go io.Copy(io.Discard, resp.Body)
	if st, _ := call(t, h, "POST", "/api/hub/shutdown", h.AdminToken, ""); st != 200 {
		t.Fatal(st)
	}
	select {
	case <-h.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("hub did not shut down")
	}
}

func TestSSEFromHub(t *testing.T) {
	h := start(t, hub.Options{DataDir: t.TempDir(), Dev: true})
	run := devRun(t, h, `{}`)
	req, _ := http.NewRequest("GET", h.URL+"/api/stream?token="+h.AdminToken, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	got := make(chan string, 20)
	go func() {
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				close(got)
				return
			}
			got <- strings.TrimSpace(l)
		}
	}()
	waitLine := func(prefix string) string {
		for {
			select {
			case l, ok := <-got:
				if !ok {
					t.Fatal("stream closed")
				}
				if strings.HasPrefix(l, prefix) {
					return l
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("timeout waiting for %q", prefix)
			}
		}
	}
	waitLine("event: hello")
	ingest(t, h, run, "heartbeat", "m1", "")
	waitLine("event: baton")
	d := waitLine("data: ")
	if !bytes.Contains([]byte(d), []byte(`"type":"heartbeat"`)) {
		t.Fatal(d)
	}
}
