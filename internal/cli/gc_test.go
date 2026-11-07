package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/daemon"
)

// gcHubStub answers POST /api/gc with one removable and one skipped item; calls records
// each request body.
func gcHubStub(t *testing.T, items string) (*fakeDaemon, string, *[]map[string]bool) {
	t.Helper()
	var calls []map[string]bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/gc" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(404)
			return
		}
		var b map[string]bool
		_ = json.NewDecoder(r.Body).Decode(&b)
		calls = append(calls, b)
		w.Write([]byte(`{"ok":true,"items":` + items + `}`))
	}))
	t.Cleanup(srv.Close)
	f := &fakeDaemon{state: daemon.Stopped, info: &daemon.Info{URL: srv.URL, PID: 5}}
	stubDaemon(t, f, false)
	dd := t.TempDir()
	if err := os.WriteFile(filepath.Join(dd, "admin.token"), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, dd, &calls
}

const gcItems = `[{"run_id":"r1","agent_id":"cx-1-0001","what":"worktree","reason":"merged","skipped":""},
{"run_id":"r2","agent_id":"cl-1-0002","what":"run","reason":"old","skipped":"uncommitted changes"}]`

func TestGCPromptNo(t *testing.T) {
	f, dd, calls := gcHubStub(t, gcItems)
	code, out, e := runMain("n\n", "gc", "--data-dir", dd)
	if code != 2 || !strings.Contains(e, "Remove 1 item(s)? [y/N] ") || len(*calls) != 1 || !(*calls)[0]["dry_run"] {
		t.Fatalf("code %d err %q calls %v", code, e, *calls)
	}
	if !strings.Contains(out, "remove worktree cx-1-0001 (merged)\n") || !strings.Contains(out, "skip run cl-1-0002 : uncommitted changes\n") {
		t.Fatalf("out %q", out)
	}
	if len(f.started) != 1 { // a stopped hub is started in the background, no browser
		t.Fatalf("started %v", f.started)
	}
	if len(f.opened) != 0 {
		t.Fatal("opened a browser")
	}
}

func TestGCPromptYesAndForce(t *testing.T) {
	_, dd, calls := gcHubStub(t, gcItems)
	code, out, e := runMain("y\n", "gc", "--force", "--data-dir", dd)
	if code != 0 || len(*calls) != 2 || !(*calls)[0]["dry_run"] || (*calls)[1]["dry_run"] || !(*calls)[1]["force"] {
		t.Fatalf("code %d err %q calls %v", code, e, *calls)
	}
	if !strings.Contains(out, "removed worktree cx-1-0001\n") || !strings.Contains(out, "removed 1 item(s)") {
		t.Fatalf("out %q", out)
	}
}

func TestGCYesFlagNoPrompt(t *testing.T) {
	_, dd, calls := gcHubStub(t, gcItems)
	code, _, e := runMain("", "gc", "--yes", "--data-dir", dd)
	if code != 0 || len(*calls) != 2 || strings.Contains(e, "[y/N]") {
		t.Fatalf("code %d err %q calls %v", code, e, *calls)
	}
}

func TestGCNothingToClean(t *testing.T) {
	_, dd, calls := gcHubStub(t, `[]`)
	code, out, _ := runMain("", "gc", "--data-dir", dd)
	if code != 0 || out != "nothing to clean\n" || len(*calls) != 1 {
		t.Fatalf("code %d out %q calls %v", code, out, *calls)
	}
}

func TestGCOldHub(t *testing.T) {
	f := &fakeDaemon{state: daemon.Running, info: &daemon.Info{URL: httptest.NewServer(http.NotFoundHandler()).URL}}
	stubDaemon(t, f, false)
	code, _, e := runMain("", "gc", "--data-dir", t.TempDir())
	if code != 1 || !strings.Contains(e, "older than this baton") {
		t.Fatalf("code %d err %q", code, e)
	}
}
