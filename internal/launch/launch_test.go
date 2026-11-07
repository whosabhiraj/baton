package launch_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/launch"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
	"github.com/whosabhiraj/baton/internal/store"
	"github.com/whosabhiraj/baton/internal/supervise"
)

// TestMain makes the test binary double as the supervisor (`__supervise`) and as a fake agent.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__supervise" {
		os.Exit(supervise.Run(os.Stdin))
	}
	if mode := os.Getenv("BATON_FAKE_AGENT"); mode != "" {
		fakeAgent(mode)
		return
	}
	os.Exit(m.Run())
}

func fakeAgent(mode string) {
	brief := make([]byte, 4096)
	n, _ := os.Stdin.Read(brief)
	out := func(s string) { fmt.Println(s) }
	switch mode {
	case "stream":
		out(`{"type":"system","subtype":"init","model":"m1"}`)
		out(`{"type":"assistant","message":{"id":"a","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"x.txt"}}],"usage":{"input_tokens":10,"output_tokens":5}}}`)
		out(fmt.Sprintf(`{"type":"assistant","message":{"id":"b","content":[{"type":"tool_use","name":"Bash","input":{"command":"brief=%d bt:%t kk:%t"}}],"usage":{"input_tokens":20,"output_tokens":6}}}`,
			n, os.Getenv("BATON_TOKEN") != "", os.Getenv("ANTHROPIC_API_KEY") != ""))
		out(`{"type":"result","total_cost_usd":0.5,"usage":{"input_tokens":30,"output_tokens":11}}`)
		_ = os.WriteFile("x.txt", []byte("hi\n"), 0o644)
		os.Exit(3)
	case "hang": // finishes its work, then never exits (codex under the Windows sandbox)
		out(`{"type":"result","total_cost_usd":0.1,"usage":{"input_tokens":1,"output_tokens":1}}`)
		time.Sleep(60 * time.Second)
	case "slow":
		out(`{"type":"one"}`)
		time.Sleep(3 * time.Second)
		out(`{"type":"two"}`)
	default: // sleep
		out(`{"type":"one"}`)
		time.Sleep(60 * time.Second)
	}
	os.Exit(0)
}

type fx struct {
	t      *testing.T
	repo   string
	data   string
	st     *store.Store
	rm     *runs.Manager
	cfg    config.Config
	exe    string
	launch *launch.Launcher
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := gitx.Run(context.Background(), dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...); err != nil {
		t.Fatal(err)
	}
}

func newFx(t *testing.T) *fx {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	exe, _ := os.Executable()
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "init")
	kind := func(parser, mode string) config.CustomKind {
		return config.CustomKind{Command: exe, Parser: parser, Env: map[string]string{"BATON_FAKE_AGENT": mode}}
	}
	f := &fx{t: t, repo: repo, data: t.TempDir(), exe: exe}
	f.cfg = config.Config{
		Hub: config.Hub{Mode: config.ModePropose, MaxAgentsAuto: 2, AskTimeoutS: 900, PollIntervalS: 2, HeartbeatTimeoutS: 120},
		Kinds: map[string]config.CustomKind{
			"f-stream": kind("stream-json", "stream"), "f-slow": kind("jsonl", "slow"), "f-sleep": kind("jsonl", "sleep"), "f-hang": kind("stream-json", "hang"),
		},
		Accounts: []config.Account{
			{ID: "s", Kind: "f-stream", Enabled: true, MaxConcurrent: 3, Roles: []string{"impl"}},
			{ID: "w", Kind: "f-slow", Enabled: true, MaxConcurrent: 3, Roles: []string{"impl"}},
			{ID: "z", Kind: "f-sleep", Enabled: true, MaxConcurrent: 3, Roles: []string{"impl"}},
			{ID: "n", Kind: "generic", Enabled: true, MaxConcurrent: 3, Roles: []string{"impl"}},
			{ID: "h", Kind: "f-hang", Enabled: true, MaxConcurrent: 3, Roles: []string{"impl"}},
		},
		Routing: map[string]string{},
	}
	f.open()
	return f
}

func (f *fx) open() {
	st, err := store.Open(f.data, store.Options{})
	if err != nil {
		f.t.Fatal(err)
	}
	f.st = st
	f.rm, err = runs.New(runs.Options{Repo: f.repo, DataDir: f.data, Store: st,
		Config: func() config.Config { return f.cfg }, HubURL: func() string { return "http://127.0.0.1:1" }})
	if err != nil {
		f.t.Fatal(err)
	}
	f.launch = launch.New(launch.Options{Exe: f.exe, Poll: 50 * time.Millisecond, FinishGrace: 300 * time.Millisecond})
	f.launch.Bind(st, f.rm, func() config.Config { return f.cfg })
	f.t.Cleanup(func() { f.launch.Close(); st.Close() })
}

// restart simulates a hub restart: launcher and store closed, then reopened.
func (f *fx) restart() {
	f.launch.Close()
	f.st.Close()
	f.open()
}

func (f *fx) spawn(account string) *runs.SpawnResult {
	f.t.Helper()
	res, perr := f.rm.Spawn(context.Background(), runs.SpawnRequest{Account: account, Role: "impl", Task: "t", Effort: "high"})
	if perr != nil {
		f.t.Fatal(perr)
	}
	return res
}

func (f *fx) of(runID, typ string) []proto.StoredEvent {
	evs, err := f.st.Get(runID).Events(0)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []proto.StoredEvent
	for _, e := range evs {
		if typ == "" || e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (f *fx) wait(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	f.t.Fatalf("timeout waiting for %s", what)
}

func num(v any) float64 {
	var f float64
	_, _ = fmt.Sscan(fmt.Sprint(v), &f)
	return f
}

func TestEndToEndExit3(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")
	f := newFx(t)
	res := f.spawn("s")
	if err := f.launch.Start(*res); err != nil {
		t.Fatal(err)
	}
	f.wait("exit", func() bool { return len(f.of(res.RunID, "exit")) == 1 })
	ex := f.of(res.RunID, "exit")[0].Payload
	if num(ex["code"]) != 3 {
		t.Fatalf("exit payload %v", ex)
	}
	if ds, _ := ex["diff_stat"].(string); !strings.Contains(ds, "1 uncommitted") {
		t.Fatalf("diff_stat %v", ex["diff_stat"])
	}
	acts := f.of(res.RunID, "activity")
	if len(acts) != 2 || acts[0].Payload["tool"] != "edit" || acts[0].Src != proto.SrcAdapter {
		t.Fatalf("activity %+v", acts)
	}
	detail, _ := acts[1].Payload["detail"].(string)
	if !strings.Contains(detail, "bt:true kk:false") || strings.Contains(detail, "brief=0 ") {
		t.Fatalf("env/brief not as expected: %q", detail)
	}
	us := f.of(res.RunID, "usage")
	if len(us) != 3 || num(us[2].Payload["cost_usd"]) != 0.5 || num(us[1].Payload["input_tokens"]) != 30 {
		t.Fatalf("usage %+v", us)
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "brief.md")); err != nil {
		t.Fatal(err)
	}
	if got := f.st.Get(res.RunID).Record().Status; got != proto.StatusExited {
		t.Fatalf("status %v", got)
	}
}

func TestKill(t *testing.T) {
	f := newFx(t)
	res := f.spawn("z")
	if err := f.launch.Start(*res); err != nil {
		t.Fatal(err)
	}
	f.wait("first line", func() bool { return len(f.of(res.RunID, "activity")) == 1 })
	if err := f.launch.Kill(res.RunID); err != nil {
		t.Fatal(err)
	}
	if err := f.launch.Kill(res.RunID); err != nil { // idempotent
		t.Fatal(err)
	}
	f.wait("exit", func() bool { return len(f.of(res.RunID, "exit")) == 1 })
	ki, ei := -1, -1
	for i, e := range f.of(res.RunID, "") {
		switch e.Type {
		case "killed":
			ki = i
		case "exit":
			ei = i
		}
	}
	if len(f.of(res.RunID, "killed")) != 1 || ki < 0 || ki > ei {
		t.Fatalf("killed must be recorded once, before exit: %d %d", ki, ei)
	}
	if supervise.Alive(res.RunDir) {
		t.Fatal("agent still alive")
	}
}

func TestAdoptAfterRestart(t *testing.T) {
	f := newFx(t)
	res := f.spawn("w")
	if err := f.launch.Start(*res); err != nil {
		t.Fatal(err)
	}
	f.wait("first line", func() bool { return len(f.of(res.RunID, "activity")) == 1 })
	f.restart() // the agent keeps running (about 3 s)
	f.launch.Adopt()
	f.wait("exit", func() bool { return len(f.of(res.RunID, "exit")) == 1 })
	acts := f.of(res.RunID, "activity")
	if len(acts) != 2 || acts[0].Payload["detail"] != "one" || acts[1].Payload["detail"] != "two" {
		t.Fatalf("activity after adopt: %+v", acts)
	}
	f.restart() // a finished run is not reported again
	f.launch.Adopt()
	time.Sleep(400 * time.Millisecond)
	if n := len(f.of(res.RunID, "exit")); n != 1 {
		t.Fatalf("exit recorded %d times", n)
	}
	if n := len(f.of(res.RunID, "activity")); n != 2 {
		t.Fatalf("activity duplicated: %d", n)
	}
}

func TestAdoptRetailNoDuplicates(t *testing.T) {
	f := newFx(t)
	res := f.spawn("s")
	if err := f.launch.Start(*res); err != nil {
		t.Fatal(err)
	}
	f.wait("exit", func() bool { return len(f.of(res.RunID, "exit")) == 1 })
	// Hub died after the events were stored but before the offset and exit marker were saved.
	f.launch.Close()
	_ = os.Remove(filepath.Join(res.RunDir, "exit.reported"))
	_ = os.Remove(filepath.Join(res.RunDir, "stdout.offset"))
	f.st.Close()
	f.open()
	f.launch.Adopt()
	time.Sleep(600 * time.Millisecond)
	if n := len(f.of(res.RunID, "exit")); n != 1 {
		t.Fatalf("exit recorded %d times", n)
	}
	if n := len(f.of(res.RunID, "usage")); n != 3 {
		t.Fatalf("usage events %d", n)
	}
	if n := len(f.of(res.RunID, "activity")); n != 2 {
		t.Fatalf("activity events %d", n)
	}
}

func TestLostSupervisor(t *testing.T) {
	f := newFx(t)
	res := f.spawn("z")
	// A supervisor.json for a process that is not there, and no exit.json.
	_ = os.MkdirAll(res.RunDir, 0o755)
	_ = os.WriteFile(filepath.Join(res.RunDir, "supervisor.json"), []byte(`{"pid":1,"child_pid":0}`), 0o644)
	f.launch.Adopt()
	f.wait("lost exit", func() bool { return len(f.of(res.RunID, "exit")) == 1 })
	p := f.of(res.RunID, "exit")[0].Payload
	if num(p["code"]) != -1 || p["detail"] != "lost after hub restart" {
		t.Fatalf("%v", p)
	}
}

func TestStartErrors(t *testing.T) {
	f := newFx(t)
	res := f.spawn("n") // generic without kinds.generic
	err := f.launch.Start(*res)
	if err == nil || !strings.Contains(err.Error(), "kinds.generic") {
		t.Fatalf("got %v", err)
	}
}

// TestHungAfterFinish: a CLI that prints its final line and then hangs is ended by Baton, and
// the exit is recorded as a normal one (code 0), not as a kill.
func TestHungAfterFinish(t *testing.T) {
	f := newFx(t)
	res := f.spawn("h")
	if err := f.launch.Start(*res); err != nil {
		t.Fatal(err)
	}
	f.wait("exit", func() bool { return len(f.of(res.RunID, "exit")) == 1 })
	ex := f.of(res.RunID, "exit")[0].Payload
	if num(ex["code"]) != 0 || ex["killed"] != nil || !strings.Contains(fmt.Sprint(ex["detail"]), "did not exit") {
		t.Fatalf("exit: %v", ex)
	}
	if len(f.of(res.RunID, "killed")) != 0 {
		t.Fatal("a reaped CLI must not show as killed")
	}
	if len(f.of(res.RunID, "finished")) != 0 {
		t.Fatal("finished is never stored")
	}
}
