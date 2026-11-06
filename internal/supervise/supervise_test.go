package supervise

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/proc"
)

// TestMain doubles as the supervisor (`__supervise`) and as the fake agent (BATON_TEST_AGENT).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__supervise" {
		os.Exit(Run(os.Stdin))
	}
	if mode := os.Getenv("BATON_TEST_AGENT"); mode != "" {
		agent(mode)
		return
	}
	os.Exit(m.Run())
}

func agent(mode string) {
	switch mode {
	case "echo":
		b, _ := io.ReadAll(os.Stdin)
		fmt.Printf("stdin:%s\n", b)
		fmt.Printf("KEY=%s\nBATON_X=%s\n", os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("BATON_X"))
		fmt.Fprintln(os.Stderr, "oops")
		os.Exit(3)
	case "tree":
		c := exec.Command(os.Args[0])
		c.Env = append(os.Environ(), "BATON_TEST_AGENT=sleep")
		if err := c.Start(); err != nil {
			os.Exit(9)
		}
		os.WriteFile(os.Getenv("BATON_TEST_PIDFILE"), []byte(strconv.Itoa(c.Process.Pid)), 0o644)
		time.Sleep(60 * time.Second)
	case "short":
		time.Sleep(1500 * time.Millisecond)
	default:
		time.Sleep(60 * time.Second)
	}
	os.Exit(0)
}

func launch(t *testing.T, mode string, extra map[string]string) (string, Spec, int) {
	t.Helper()
	dir := t.TempDir()
	baton := map[string]string{"BATON_TEST_AGENT": mode}
	for k, v := range extra {
		baton[k] = v
	}
	spec := Spec{Argv: []string{os.Args[0]}, Env: proc.BuildEnv(nil, nil, baton), Dir: dir,
		RunDir: filepath.Join(dir, "run"), KillGraceS: 1}
	if spec.StdinFile = extra["stdin"]; spec.StdinFile == "" {
		spec.StdinFile = filepath.Join(dir, "brief.txt")
		os.WriteFile(spec.StdinFile, []byte("hello brief"), 0o644)
	}
	pid, err := Launch(os.Args[0], spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { RequestKill(spec.RunDir, 3*time.Second) })
	return spec.RunDir, spec, pid
}

func awaitExit(t *testing.T, run string, d time.Duration) *Exit {
	t.Helper()
	if !waitExit(run, d) {
		t.Fatal("no exit.json")
	}
	e, _ := ReadExit(run)
	return e
}

func TestExitCodeFilesAndEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-leak")
	run, _, _ := launch(t, "echo", map[string]string{"BATON_X": "42"})
	e := awaitExit(t, run, 10*time.Second)
	if e.Code != 3 || e.Killed || e.EndedAt == "" {
		t.Errorf("exit %+v", e)
	}
	out, _ := os.ReadFile(filepath.Join(run, "stdout.jsonl"))
	s := strings.ReplaceAll(string(out), "\r", "")
	if !strings.Contains(s, "stdin:hello brief") {
		t.Errorf("stdin not delivered: %q", s)
	}
	if !strings.Contains(s, "KEY=\n") || !strings.Contains(s, "BATON_X=42\n") {
		t.Errorf("env: %q", s)
	}
	if b, _ := os.ReadFile(filepath.Join(run, "stderr.log")); !strings.Contains(string(b), "oops") {
		t.Errorf("stderr: %q", b)
	}
	if Alive(run) {
		t.Error("alive after exit")
	}
}

func TestKillTreeKillsGrandchild(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "gc.pid")
	run, _, _ := launch(t, "tree", map[string]string{"BATON_TEST_PIDFILE": pidfile})
	var gc int
	for i := 0; i < 100 && gc == 0; i++ {
		if b, err := os.ReadFile(pidfile); err == nil {
			gc, _ = strconv.Atoi(string(b))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if gc == 0 || !pidAlive(gc) {
		t.Fatalf("grandchild not running (%d)", gc)
	}
	if !Alive(run) {
		t.Fatal("agent should be alive")
	}
	if err := RequestKill(run, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	e, ok := ReadExit(run)
	if !ok || !e.Killed {
		t.Fatalf("exit %+v", e)
	}
	for i := 0; i < 100 && pidAlive(gc); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if pidAlive(gc) {
		t.Errorf("grandchild %d survived", gc)
	}
	if Alive(run) {
		t.Error("alive after kill")
	}
}

func TestSupervisorOutlivesLauncher(t *testing.T) {
	run, _, _ := launch(t, "short", nil)
	if _, ok := ReadExit(run); ok {
		t.Fatal("exit too early")
	}
	if !Alive(run) {
		t.Fatal("expected alive while running")
	}
	e := awaitExit(t, run, 10*time.Second)
	if e.Code != 0 || e.DurationS < 1 {
		t.Errorf("exit %+v", e)
	}
	if Alive(run) {
		t.Error("alive after exit")
	}
}

func TestPidReuseGuard(t *testing.T) {
	run, _, _ := launch(t, "sleep", nil)
	in, err := readInfo(run)
	if err != nil {
		t.Fatal(err)
	}
	orig := *in
	in.ChildStart = "bogus"
	writeJSON(supPath(run), in)
	if Alive(run) {
		t.Error("mismatched start time must not count as alive")
	}
	writeJSON(supPath(run), &orig)
	if !Alive(run) {
		t.Error("restored info should be alive")
	}
}

func TestRequestKillFallbackWhenSupervisorDead(t *testing.T) {
	run, _, supPID := launch(t, "sleep", nil)
	in, _ := readInfo(run)
	p, _ := os.FindProcess(supPID)
	p.Kill()
	time.Sleep(300 * time.Millisecond)
	if err := RequestKill(run, 600*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	e, ok := ReadExit(run)
	if !ok || !e.Killed || e.Code != -1 {
		t.Fatalf("exit %+v", e)
	}
	for i := 0; i < 60 && pidAlive(in.ChildPID); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if pidAlive(in.ChildPID) {
		t.Error("agent survived")
	}
}

func TestLaunchMissingExecutable(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{Argv: []string{"definitely-not-a-real-binary-xyz"}, Env: proc.BuildEnv(nil, nil, nil),
		Dir: dir, RunDir: filepath.Join(dir, "run")}
	_, err := Launch(os.Args[0], spec)
	if err == nil || !strings.Contains(err.Error(), "cannot find") {
		t.Fatalf("err = %v", err)
	}
}
