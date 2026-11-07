package codexapp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain lets the test binary act as a fake `codex app-server`.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_APP_SERVER") == "1" {
		fakeAppServer()
		return
	}
	os.Exit(m.Run())
}

// fakeAppServer: answers initialize / thread/start / turn/start, runs one item, asks for one
// command approval, reports the decision as a notification, completes the turn, then hangs
// (the driver must end it). Env leaks are reported in the first notification.
func fakeAppServer() {
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var m map[string]any
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		id, method := m["id"], fmt.Sprint(m["method"])
		switch method {
		case "initialize":
			out.Encode(map[string]any{"id": id, "result": map[string]any{"userAgent": "fake"}})
		case "thread/start":
			out.Encode(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th1"}}})
		case "turn/start":
			text := ""
			if p, ok := m["params"].(map[string]any); ok {
				if in, ok := p["input"].([]any); ok && len(in) > 0 {
					text, _ = in[0].(map[string]any)["text"].(string)
				}
			}
			out.Encode(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "tu1"}}})
			out.Encode(map[string]any{"method": "turn/started", "params": map[string]any{"brief": text,
				"leak": os.Getenv(EnvToken) + os.Getenv("BATON_TOKEN") + os.Getenv(EnvURL)}})
			out.Encode(map[string]any{"method": "item/started", "params": map[string]any{"item": map[string]any{"type": "commandExecution", "command": "curl x"}}})
			out.Encode(map[string]any{"id": 99, "method": "item/commandExecution/requestApproval",
				"params": map[string]any{"command": "curl https://example.com", "cwd": "/w", "reason": "needs network", "threadId": "th1", "turnId": "tu1", "itemId": "i1", "startedAtMs": 1}})
			out.Encode(map[string]any{"id": 98, "method": "item/tool/requestUserInput", "params": map[string]any{}})
		default:
			if method == "<nil>" && id != nil { // a response from the driver
				if fmt.Sprint(id) == "99" {
					res, _ := m["result"].(map[string]any)
					out.Encode(map[string]any{"method": "fake/decision", "params": map[string]any{"decision": res["decision"]}})
					out.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"id": "tu1", "status": "completed"}}})
				}
				if fmt.Sprint(id) == "98" {
					out.Encode(map[string]any{"method": "fake/userinput", "params": map[string]any{"error": m["error"] != nil}})
				}
			}
		}
	}
	time.Sleep(time.Minute) // hang like the real one may: the driver must not wait for us
}

type fakeHub struct {
	mu        sync.Mutex
	decision  string // what GET returns after the first poll
	created   []map[string]any
	cancelled bool
	auth      string
}

func (f *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/runs/r1/access":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.created = append(f.created, b)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "approval": map[string]any{"id": "ap1", "status": "pending"}})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/runs/r1/access/ap1"):
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": f.decision})
	case r.Method == "POST" && r.URL.Path == "/api/runs/r1/access/ap1/cancel":
		f.cancelled = true
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": "rejected"})
	default:
		http.Error(w, "nope", 404)
	}
}

func runDriver(t *testing.T, hub *fakeHub, timeout time.Duration) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	exe, _ := os.Executable()
	t.Setenv("FAKE_APP_SERVER", "1")
	t.Setenv(EnvToken, "bt_run_secret") // in our env too: childEnv must strip it for codex
	t.Setenv("BATON_TOKEN", "bt_run_secret2")
	env := map[string]string{EnvURL: srv.URL, EnvToken: "bt_run_secret", "BATON_RUN_ID": "r1", EnvCLI: exe}
	var out, errb bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(Options{Args: []string{"-c", "x=1"}, Stdin: strings.NewReader("THE BRIEF"), Stdout: &out, Stderr: &errb,
			Getenv: func(k string) string { return env[k] }, AskTimeout: timeout, Version: "test"})
	}()
	select {
	case code := <-done:
		return code, out.String(), errb.String()
	case <-time.After(20 * time.Second):
		t.Fatal("driver did not finish")
	}
	return 0, "", ""
}

func TestDriverApproved(t *testing.T) {
	hub := &fakeHub{decision: "approved"}
	code, out, errs := runDriver(t, hub, 5*time.Second)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	for _, want := range []string{`"brief":"THE BRIEF"`, `"leak":""`, `"method":"item/started"`, `"decision":"accept"`,
		`"method":"turn/completed"`, `"method":"baton/access"`, `"status":"approved"`, `"error":true`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %s:\n%s", want, out)
		}
	}
	if len(hub.created) != 1 || hub.created[0]["kind"] != "command" || hub.created[0]["summary"] != "needs network" ||
		hub.auth != "Bearer bt_run_secret" {
		t.Fatalf("hub saw %+v auth %q", hub.created, hub.auth)
	}
	if d, _ := hub.created[0]["detail"].(map[string]any); d["command"] != "curl https://example.com" || d["reason"] != nil {
		t.Fatalf("detail %+v", hub.created[0]["detail"])
	}
}

func TestDriverRejectedAndTimeout(t *testing.T) {
	if _, out, _ := runDriver(t, &fakeHub{decision: "rejected"}, 5*time.Second); !strings.Contains(out, `"decision":"decline"`) {
		t.Fatalf("rejected:\n%s", out)
	}
	hub := &fakeHub{decision: "pending"}
	code, out, _ := runDriver(t, hub, 300*time.Millisecond)
	if code != 0 || !strings.Contains(out, `"decision":"decline"`) || !strings.Contains(out, `"status":"expired"`) || !hub.cancelled {
		t.Fatalf("timeout: code %d cancelled %v\n%s", code, hub.cancelled, out)
	}
}

func TestDriverNoHub(t *testing.T) {
	exe, _ := os.Executable()
	t.Setenv("FAKE_APP_SERVER", "1")
	env := map[string]string{"BATON_RUN_ID": "r1", EnvCLI: exe} // no URL/token: every request is a no
	var out, errb bytes.Buffer
	code := Run(Options{Stdin: strings.NewReader("b"), Stdout: &out, Stderr: &errb, Getenv: func(k string) string { return env[k] }, AskTimeout: time.Second})
	if code != 0 || !strings.Contains(out.String(), `"decision":"decline"`) || !strings.Contains(errb.String(), "no hub URL") {
		t.Fatalf("code %d\n%s\n%s", code, out.String(), errb.String())
	}
}

func TestChildEnv(t *testing.T) {
	got := childEnv([]string{"PATH=x", "BATON_DRIVER_TOKEN=s", "baton_token=s2", "BATON_URL=u", "BATON_RUN_ID=r", "BATON_CODEX_CLI=c"})
	if strings.Join(got, ";") != "PATH=x;BATON_RUN_ID=r" {
		t.Fatal(got)
	}
}
