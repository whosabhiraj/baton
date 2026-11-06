// Package supervise runs each agent under a tiny detached supervisor process (`baton __supervise`)
// so agents survive a hub restart and their exit status is never lost (GO_HUB_DESIGN §6.1, §6.3).
//
// Files in the run directory:
//
//	stdout.jsonl, stderr.log  agent output (the agent's stdin is Spec.StdinFile)
//	supervisor.json           {pid, child_pid, child_start, started_at} (+ sup_start)
//	control/kill              empty; its presence asks the supervisor to kill the agent tree
//	exit.json                 final status, written atomically
package supervise

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Spec is the launch specification sent to the supervisor on stdin. It holds secrets (env), so it
// is never written to disk or argv.
type Spec struct {
	Argv       []string `json:"argv"`
	Env        []string `json:"env"`
	Dir        string   `json:"dir"`
	StdinFile  string   `json:"stdin_file"`
	RunDir     string   `json:"run_dir"` // absolute
	KillGraceS int      `json:"kill_grace_s"`
}

// Exit is the content of exit.json.
type Exit struct {
	Code      int     `json:"code"`
	Signal    *string `json:"signal"`
	Killed    bool    `json:"killed"`
	EndedAt   string  `json:"ended_at"`
	DurationS float64 `json:"duration_s"`
}

type info struct {
	PID        int    `json:"pid"`
	ChildPID   int    `json:"child_pid"`
	ChildStart string `json:"child_start"`
	StartedAt  string `json:"started_at"`
	SupStart   string `json:"sup_start,omitempty"`
}

const pollEvery = 500 * time.Millisecond

func supPath(d string) string  { return filepath.Join(d, "supervisor.json") }
func exitPath(d string) string { return filepath.Join(d, "exit.json") }
func killPath(d string) string { return filepath.Join(d, "control", "kill") }

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	for i := 0; ; i++ {
		if err = os.Rename(tmp, path); err == nil || runtime.GOOS != "windows" || i >= 20 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func readInfo(runDir string) (*info, error) {
	b, err := os.ReadFile(supPath(runDir))
	if err != nil {
		return nil, err
	}
	var in info
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// ReadExit returns the run's exit status once exit.json exists.
func ReadExit(runDir string) (*Exit, bool) {
	b, err := os.ReadFile(exitPath(runDir))
	if err != nil {
		return nil, false
	}
	var e Exit
	if json.Unmarshal(b, &e) != nil {
		return nil, false
	}
	return &e, true
}

// Alive reports whether the run's agent is still running: supervisor.json exists, exit.json does
// not, and the child pid is alive with the recorded start time (defeats pid reuse).
func Alive(runDir string) bool {
	if _, ok := ReadExit(runDir); ok {
		return false
	}
	in, err := readInfo(runDir)
	if err != nil {
		return false
	}
	return childMatches(in)
}

func childMatches(in *info) bool {
	if in.ChildPID <= 0 || !pidAlive(in.ChildPID) {
		return false
	}
	st, ok := procStart(in.ChildPID)
	return ok && st == in.ChildStart
}

// Launch starts `<exe> __supervise` detached, hands it spec on stdin and returns the supervisor's
// pid once supervisor.json exists (up to ~10 s). If the supervisor dies first, its stderr is
// returned as the error.
func Launch(exe string, spec Spec) (int, error) {
	if spec.RunDir == "" || !filepath.IsAbs(spec.RunDir) {
		return 0, errors.New("supervise: RunDir must be an absolute path")
	}
	if err := os.MkdirAll(filepath.Join(spec.RunDir, "control"), 0o755); err != nil {
		return 0, err
	}
	os.Remove(supPath(spec.RunDir))
	os.Remove(exitPath(spec.RunDir))
	os.Remove(killPath(spec.RunDir))
	body, err := json.Marshal(spec)
	if err != nil {
		return 0, err
	}
	errPath := filepath.Join(spec.RunDir, "supervisor.err")
	ef, err := os.Create(errPath)
	if err != nil {
		return 0, err
	}
	defer ef.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, "__supervise")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pr, ef, ef
	cmd.Env = minimalEnv()
	cmd.Dir = spec.RunDir
	detach(cmd)
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return 0, fmt.Errorf("supervise: start supervisor: %w", err)
	}
	pr.Close()
	_, werr := pw.Write(body)
	pw.Close()
	died := make(chan error, 1)
	go func() { died <- cmd.Wait() }()
	if werr != nil {
		return 0, fmt.Errorf("supervise: send spec: %w", werr)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case werr := <-died:
			if _, err := readInfo(spec.RunDir); err == nil { // started and finished very fast
				return cmd.Process.Pid, nil
			}
			msg, _ := os.ReadFile(errPath)
			return 0, fmt.Errorf("supervise: supervisor exited (%v): %s", werr, strings.TrimSpace(string(msg)))
		case <-deadline:
			return 0, errors.New("supervise: timed out waiting for supervisor.json")
		case <-time.After(25 * time.Millisecond):
			if _, err := readInfo(spec.RunDir); err == nil {
				return cmd.Process.Pid, nil
			}
		}
	}
}

func minimalEnv() []string {
	var out []string
	for _, k := range []string{"SystemRoot", "SystemDrive", "ComSpec", "TEMP", "TMP", "PATH", "HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// resolveExe finds argv0 using the PATH/PATHEXT of the spec env, not the supervisor's own.
func resolveExe(argv0 string, env []string) (string, error) {
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		switch k := kv[:i]; {
		case strings.EqualFold(k, "PATH") && (runtime.GOOS == "windows" || k == "PATH"):
			os.Setenv("PATH", kv[i+1:])
		case strings.EqualFold(k, "PATHEXT") && runtime.GOOS == "windows":
			os.Setenv("PATHEXT", kv[i+1:])
		}
	}
	p, err := exec.LookPath(argv0)
	if err != nil {
		return "", fmt.Errorf("cannot find %q on the agent PATH: %w", argv0, err)
	}
	return p, nil
}

// Run is the body of `baton __supervise`: it reads a Spec from stdin, runs the agent, honours
// control/kill, writes exit.json and returns the process exit code (0 unless setup failed).
func Run(stdin io.Reader) int {
	var spec Spec
	if err := json.NewDecoder(stdin).Decode(&spec); err != nil {
		fmt.Fprintln(os.Stderr, "supervise: bad spec:", err)
		return 1
	}
	if len(spec.Argv) == 0 || spec.RunDir == "" {
		fmt.Fprintln(os.Stderr, "supervise: spec needs argv and run_dir")
		return 1
	}
	if err := run(spec); err != nil {
		fmt.Fprintln(os.Stderr, "supervise:", err)
		return 1
	}
	return 0
}

func run(spec Spec) error {
	rd := spec.RunDir
	if err := os.MkdirAll(filepath.Join(rd, "control"), 0o755); err != nil {
		return err
	}
	os.Remove(killPath(rd))
	exe, err := resolveExe(spec.Argv[0], spec.Env)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, spec.Argv[1:]...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	if spec.StdinFile != "" {
		f, err := os.Open(spec.StdinFile)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd.Stdin = f
	}
	of, err := os.Create(filepath.Join(rd, "stdout.jsonl"))
	if err != nil {
		return err
	}
	defer of.Close()
	ef, err := os.Create(filepath.Join(rd, "stderr.log"))
	if err != nil {
		return err
	}
	defer ef.Close()
	cmd.Stdout, cmd.Stderr = of, ef
	childAttr(cmd)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}
	tree := newTree(cmd.Process)
	defer tree.close()
	pid := cmd.Process.Pid
	cs, _ := procStart(pid)
	ss, _ := procStart(os.Getpid())
	if err := writeJSON(supPath(rd), info{PID: os.Getpid(), ChildPID: pid, ChildStart: cs,
		StartedAt: start.Format(time.RFC3339Nano), SupStart: ss}); err != nil {
		tree.kill(nil, 0)
		return err
	}

	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	killed := false
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
wait:
	for {
		select {
		case <-done:
			break wait
		case <-tick.C:
			if _, err := os.Stat(killPath(rd)); err == nil {
				killed = true
				grace := spec.KillGraceS
				if grace <= 0 {
					grace = 5
				}
				tree.kill(done, time.Duration(grace)*time.Second)
				<-done
				break wait
			}
		}
	}
	code, sig := exitInfo(cmd.ProcessState)
	end := time.Now()
	return writeJSON(exitPath(rd), Exit{Code: code, Signal: sig, Killed: killed,
		EndedAt: end.Format(time.RFC3339Nano), DurationS: end.Sub(start).Seconds()})
}

// RequestKill asks the supervisor to kill the agent tree and waits up to timeout for exit.json.
// If the supervisor does not answer it kills the tree itself, writes exit.json if still missing
// (code -1, signal SIGKILL, killed true) and, on Windows as a last resort, runs taskkill /T /F.
func RequestKill(runDir string, timeout time.Duration) error {
	if _, ok := ReadExit(runDir); ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(killPath(runDir)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(killPath(runDir), nil, 0o644); err != nil {
		return err
	}
	if waitExit(runDir, timeout) {
		return nil
	}
	in, err := readInfo(runDir)
	if err != nil {
		return fmt.Errorf("supervise: no exit and no supervisor.json: %w", err)
	}
	if childMatches(in) {
		forceKill(in)
		waitExit(runDir, 2*time.Second)
	}
	if _, ok := ReadExit(runDir); !ok {
		if childMatches(in) {
			lastResort(in.ChildPID)
			time.Sleep(300 * time.Millisecond)
		}
		sig := "SIGKILL"
		now := time.Now()
		dur := 0.0
		if t, err := time.Parse(time.RFC3339Nano, in.StartedAt); err == nil {
			dur = now.Sub(t).Seconds()
		}
		if err := writeJSON(exitPath(runDir), Exit{Code: -1, Signal: &sig, Killed: true,
			EndedAt: now.Format(time.RFC3339Nano), DurationS: dur}); err != nil {
			return err
		}
	}
	if childMatches(in) {
		return fmt.Errorf("supervise: agent pid %d still alive after kill", in.ChildPID)
	}
	return nil
}

func waitExit(runDir string, timeout time.Duration) bool {
	end := time.Now().Add(timeout)
	for {
		if _, ok := ReadExit(runDir); ok {
			return true
		}
		if !time.Now().Before(end) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
