package hub_test

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/hub"
)

const acct = `hub:
  mode: auto
accounts:
  - id: a1
    kind: generic
    roles: [implementer]
    max_concurrent: 1
`

func p2opts(t *testing.T) (hub.Options, string) {
	t.Helper()
	d := t.TempDir()
	gp := filepath.Join(d, "config.yaml")
	if err := os.WriteFile(gp, []byte(acct), 0o644); err != nil {
		t.Fatal(err)
	}
	return hub.Options{DataDir: filepath.Join(d, "data"), AdminToken: "tok", GlobalConfig: gp,
		ProjectConfig: filepath.Join(d, "none.yaml"), ConfigPoll: 30 * time.Millisecond, IdleStop: -1}, gp
}

// openStream returns a line channel for GET /api/stream.
func openStream(t *testing.T, h *hub.Hub) (<-chan string, func()) {
	t.Helper()
	req, _ := http.NewRequest("GET", h.URL+"/api/stream", nil)
	req.Header.Set("Authorization", "Bearer "+h.AdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			select {
			case ch <- sc.Text():
			default:
			}
		}
		close(ch)
	}()
	return ch, func() { resp.Body.Close() }
}

func waitLine(t *testing.T, ch <-chan string, sub string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed waiting for %q", sub)
			}
			if strings.Contains(l, sub) {
				return l
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %q", sub)
		}
	}
}

func TestConfigReloadNoticeAndLiveState(t *testing.T) {
	o, gp := p2opts(t)
	h := start(t, o)
	ch, stop := openStream(t, h)
	defer stop()
	waitLine(t, ch, "event: hello")

	st, m := call(t, h, "GET", "/api/state", "tok", "")
	if st != 200 || m["mode"] != "auto" || m["config_ok"] != true || m["instance"] == "" {
		t.Fatalf("state: %d %v", st, m)
	}
	if accs, _ := m["accounts"].([]any); len(accs) != 1 {
		t.Fatalf("accounts: %v", m["accounts"])
	}

	if err := os.WriteFile(gp, []byte("hub:\n  port: 99999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitLine(t, ch, "event: hub")
	// data line follows the event line
	l := waitLine(t, ch, `"type":"config_reloaded"`)
	if !strings.Contains(l, `"ok":false`) {
		t.Fatalf("notice: %s", l)
	}
	st, m = call(t, h, "GET", "/api/config", "tok", "")
	if st != 200 || m["ok"] != false || m["issues"] == nil {
		t.Fatalf("config: %d %v", st, m)
	}
	// old config still in effect
	_, m = call(t, h, "GET", "/api/state", "tok", "")
	if m["mode"] != "auto" || m["config_ok"] != false {
		t.Fatalf("state after break: %v", m)
	}
	if err := os.WriteFile(gp, []byte(acct), 0o644); err != nil {
		t.Fatal(err)
	}
	l = waitLine(t, ch, `"type":"config_reloaded"`)
	if !strings.Contains(l, `"ok":true`) {
		t.Fatalf("notice: %s", l)
	}
}

func TestIdleStop(t *testing.T) {
	o, _ := p2opts(t)
	o.IdleStop = 300 * time.Millisecond
	h := start(t, o)
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("idle hub did not stop")
	}
}

func TestNotIdleWithSubscriberOrRun(t *testing.T) {
	o, _ := p2opts(t)
	o.IdleStop = 300 * time.Millisecond
	h := start(t, o)
	ch, stop := openStream(t, h)
	waitLine(t, ch, "event: hello")
	select {
	case <-h.Done():
		t.Fatal("stopped with an SSE client connected")
	case <-time.After(1200 * time.Millisecond):
	}
	stop()
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop after the client left")
	}
}

func TestIdleBlockedByLiveRun(t *testing.T) {
	o, _ := p2opts(t)
	o.IdleStop = 300 * time.Millisecond
	o.Dev = true
	h := start(t, o)
	devRun(t, h, `{"role":"x"}`)
	select {
	case <-h.Done():
		t.Fatal("stopped with a live run")
	case <-time.After(900 * time.Millisecond):
	}
}

func TestSpawnWithoutRepo(t *testing.T) {
	o, _ := p2opts(t)
	h := start(t, o)
	st, m := call(t, h, "POST", "/api/spawn", "tok", `{"account":"a1","role":"implementer"}`)
	if st != 409 || m["code"] != "NOT_A_REPO" {
		t.Fatalf("%d %v", st, m)
	}
}

func TestAuditAndActor(t *testing.T) {
	o, _ := p2opts(t)
	o.Dev = true
	h := start(t, o)
	m := devRun(t, h, `{"role":"implementer"}`)
	rid := m["run_id"].(string)
	if st, r := call(t, h, "POST", "/api/runs/"+rid+"/message", "tok", `{"text":"hi"}`); st != 200 {
		t.Fatal(st, r)
	}
	_, a := call(t, h, "GET", "/api/audit?limit=10", "tok", "")
	es, _ := a["entries"].([]any)
	var acts []string
	for _, e := range es {
		em := e.(map[string]any)
		acts = append(acts, em["action"].(string)+"@"+em["actor"].(string))
	}
	got := strings.Join(acts, ",")
	if !strings.Contains(got, "dev.run@human") || !strings.Contains(got, "deliver.message@human") {
		t.Fatalf("audit: %s", got)
	}
}

// TestIdleStopWithConductor: the conductor's own bus listener (auto-wake) is not a viewer, so
// a repo hub with a conductor still stops when idle.
func TestIdleStopWithConductor(t *testing.T) {
	o, _ := p2opts(t)
	o.IdleStop = 300 * time.Millisecond
	o.Repo = filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(o.Repo, 0o755)
	if err := gitx.Init(o.Repo); err != nil {
		t.Fatal(err)
	}
	o.ConductorExe, o.ConductorCLI = "baton", "baton-test-no-such-cli"
	h := start(t, o)
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("idle hub with a conductor did not stop")
	}
}
