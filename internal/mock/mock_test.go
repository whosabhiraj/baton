package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/hub"
)

func call(t *testing.T, h *hub.Hub, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.AdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.NewDecoder(bytes.NewReader(b)).Decode(&m)
	return resp.StatusCode, m
}

func until(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for !f() {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func stateRuns(t *testing.T, h *hub.Hub) []map[string]any {
	_, m := call(t, h, "GET", "/api/state", "")
	var out []map[string]any
	for _, r := range m["runs"].([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

func allEndedN(rs []map[string]any, n int) bool {
	if len(rs) < n {
		return false
	}
	for _, r := range rs {
		switch r["status"] {
		case "done", "failed", "killed", "exited":
		default:
			return false
		}
	}
	return true
}

func TestMockScenario(t *testing.T) {
	t.Setenv("BATON_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	type hubEng struct {
		h *hub.Hub
		e *Engine
	}
	ready := make(chan hubEng, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- Run(ctx, Options{Speed: 50, Seed: 1, Once: true}, func(h *hub.Hub, e *Engine) { ready <- hubEng{h, e} })
	}()
	var he hubEng
	select {
	case he = <-ready:
	case err := <-finished:
		t.Fatal(err)
	case <-time.After(20 * time.Second):
		t.Fatal("hub did not start")
	}
	h, e := he.h, he.e

	until(t, 40*time.Second, "the script to finish", func() bool { return allEndedN(stateRuns(t, h), len(scripted)) })

	// every status shows up in the event logs
	seen := map[string]bool{}
	for _, r := range stateRuns(t, h) {
		_, m := call(t, h, "GET", "/api/runs/"+r["run_id"].(string)+"/events", "")
		var done, killed, exit bool
		for _, ev := range m["events"].([]any) {
			ev := ev.(map[string]any)
			p, _ := ev["payload"].(map[string]any)
			typ, _ := ev["type"].(string)
			switch typ {
			case "question":
				if p["blocking"] == true {
					seen["waiting"] = true
				}
			case "step_blocked", "paused", "stalled", "rate_limited", "usage", "activity", "artifact", "resumed":
				seen[typ] = true
			case "done":
				done = true
				seen["done:"+p["status"].(string)] = true
			case "killed":
				killed = true
				seen["killed"] = true
			case "exit":
				exit = true
			case "to_agent":
				seen["to_agent:"+p["type"].(string)] = true
			}
		}
		if exit && !done && !killed {
			seen["exited"] = true
		}
	}
	for _, w := range []string{"waiting", "step_blocked", "paused", "resumed", "stalled", "rate_limited", "done:success", "done:failed",
		"killed", "exited", "usage", "activity", "artifact", "to_agent:answer", "to_agent:message", "to_agent:pause", "to_agent:resume"} {
		if !seen[w] {
			t.Errorf("never saw %s (seen: %v)", w, seen)
		}
	}
	if errs := e.Errors(); len(errs) > 0 {
		t.Errorf("protocol errors: %v", errs)
	}

	// finished successes ask for a merge; approving marks the run merged, rejecting does not
	until(t, 10*time.Second, "3 merge approvals", func() bool {
		_, m := call(t, h, "GET", "/api/state", "")
		return len(m["approvals"].([]any)) == 3
	})
	_, m := call(t, h, "GET", "/api/state", "")
	if c := m["conductor"].(map[string]any); c["account"] != "claude-a" {
		t.Errorf("conductor: %v", c)
	}
	aps := m["approvals"].([]any)
	yes, no := aps[0].(map[string]any), aps[1].(map[string]any)
	call(t, h, "POST", "/api/approvals/"+yes["id"].(string)+"/approve", "")
	call(t, h, "POST", "/api/approvals/"+no["id"].(string)+"/reject", `{"note":"not yet"}`)
	until(t, 5*time.Second, "merged flag", func() bool {
		return string(h.Store().Get(yes["run_id"].(string)).Record().Extra["merged"]) == "true"
	})
	if h.Store().Get(no["run_id"].(string)).Record().Extra["merged"] != nil {
		t.Error("rejected run was merged")
	}

	// controls reach a live fake agent: message, plan_update (adopted), stop
	spawn := func() string {
		st, m := call(t, h, "POST", "/api/spawn", `{"account":"claude-b","role":"implementer","task":"extra"}`)
		if st != 200 {
			t.Fatal(st, m)
		}
		return m["run_id"].(string)
	}
	events := func(id string) string {
		_, m := call(t, h, "GET", "/api/runs/"+id+"/events", "")
		b, _ := json.Marshal(m["events"])
		return string(b)
	}
	a, b := spawn(), spawn()
	until(t, 10*time.Second, "agents to plan", func() bool {
		return strings.Contains(events(a), `"type":"plan"`) && strings.Contains(events(b), `"type":"plan"`)
	})
	call(t, h, "POST", "/api/runs/"+a+"/message", `{"text":"hello there"}`)
	call(t, h, "POST", "/api/runs/"+a+"/plan_update", `{"steps":[{"id":"x1","title":"Only step","acceptance":"done"}]}`)
	call(t, h, "POST", "/api/runs/"+b+"/stop", `{}`)
	until(t, 20*time.Second, "both agents to end", func() bool {
		return strings.Contains(events(a), `"type":"done"`) && strings.Contains(events(b), `"type":"done"`)
	})
	if ev := events(a); !strings.Contains(ev, "Noted message: hello there") || !strings.Contains(ev, `"status":"success"`) {
		t.Errorf("message/plan_update not honoured: %s", ev)
	}
	if ev := events(b); !strings.Contains(ev, `"status":"partial"`) {
		t.Errorf("stop not honoured: %s", ev)
	}
	if errs := e.Errors(); len(errs) > 0 {
		t.Errorf("protocol errors: %v", errs)
	}

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

}

func TestResetOnlyTouchesMockDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BATON_HOME", home)
	for _, f := range []string{"mock/data/x", "projects/keep"} {
		p := filepath.Join(home, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	if err := Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "mock")); !os.IsNotExist(err) {
		t.Error("mock dir still there")
	}
	if _, err := os.Stat(filepath.Join(home, "projects", "keep")); err != nil {
		t.Error("sibling data deleted")
	}
}
