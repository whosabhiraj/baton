package hub_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/proto"
)

// gcHub starts a repo hub with the remove_on_merge policy and one ended, merged run.
func gcHub(t *testing.T, gcEvery time.Duration) (*hub.Hub, string, string) {
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
	if err := os.WriteFile(o.ProjectConfig, []byte("hub:\n  worktrees:\n    cleanup: remove_on_merge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.Repo, o.Launcher, o.Dev, o.GCInterval = repo, &fakeLauncher{}, true, gcEvery
	h := start(t, o)
	st, r := call(t, h, "POST", "/api/spawn", "tok", `{"account":"a1","role":"implementer","task":"t"}`)
	if st != 200 {
		t.Fatal(st, r)
	}
	id := r["run_id"].(string)
	run := h.Store().Get(id)
	if _, _, e := run.IngestEnvelope(proto.Envelope{V: 1, RunID: id, AgentID: run.AgentID(), Type: "done",
		Payload: map[string]any{"status": "success", "summary": "ok"}, MsgID: "d1"}, proto.SrcCLI); e != nil {
		t.Fatal(e)
	}
	if err := run.SetExtra("merged", true); err != nil {
		t.Fatal(err)
	}
	return h, id, r["worktree"].(string)
}

func TestGCRoute(t *testing.T) {
	h, id, wt := gcHub(t, -1)
	if st, _ := call(t, h, "POST", "/api/gc", "", `{}`); st != 401 {
		t.Fatal(st)
	}
	cond := h.MintConductorToken()
	if st, _ := call(t, h, "POST", "/api/gc", cond, `{}`); st != 401 {
		t.Fatal("conductor reached gc", st)
	}
	if st, r := call(t, h, "POST", "/api/gc", "tok", `{"dry_run":"yes"}`); st != 400 {
		t.Fatal(st, r)
	}
	st, r := call(t, h, "POST", "/api/gc", "tok", `{"dry_run":true}`)
	items, _ := r["items"].([]any)
	if st != 200 || r["dry_run"] != true || len(items) != 1 || items[0].(map[string]any)["run_id"] != id {
		t.Fatal(st, r)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("dry run removed the worktree")
	}
	st, r = call(t, h, "POST", "/api/gc", "tok", ``)
	if items, _ = r["items"].([]any); st != 200 || len(items) != 1 || items[0].(map[string]any)["skipped"] != "" {
		t.Fatal(st, r)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("worktree remains")
	}
	_, a := call(t, h, "GET", "/api/audit?limit=0", "tok", "")
	found := false
	for _, e := range a["entries"].([]any) {
		if em := e.(map[string]any); em["action"] == "gc.worktree" && em["actor"] == "human" && em["target"] == id {
			found = true
		}
	}
	if !found {
		t.Fatal("no audit entry", a)
	}
}

func TestHubAutoWorktreeCleanup(t *testing.T) {
	h, id, wt := gcHub(t, 20*time.Millisecond)
	waitFor(t, "worktree removed by the automatic pass", func() bool { _, err := os.Stat(wt); return err != nil })
	if h.Store().Get(id) == nil {
		t.Fatal("run folder removed by the automatic pass")
	}
}
