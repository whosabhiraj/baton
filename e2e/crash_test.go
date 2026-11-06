package e2e

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const crashToken = "bt_adm_test_crash"

// hubProc is a separate `baton hub serve` process.
type hubProc struct {
	cmd *exec.Cmd
	URL string
}

// startUp launches `baton hub serve --dev --port 0` and reads its URL from the first stdout line.
func startUp(t *testing.T, dataDir, tokenFile string) *hubProc {
	t.Helper()
	cmd := exec.Command(binPath, "hub", "serve", "--dev", "--port", "0", "--data-dir", dataDir, "--admin-token-file", tokenFile)
	cmd.Env = baseEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start baton hub serve: %v", err)
	}
	hp := &hubProc{cmd: cmd}
	t.Cleanup(hp.kill)

	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
		_, _ = io.Copy(io.Discard, stdout) // keep draining so the hub never blocks on a full pipe
	}()
	select {
	case l, ok := <-lines:
		if !ok || !strings.HasPrefix(l, "baton hub: ") {
			t.Fatalf("first stdout line = %q, want %q prefix", l, "baton hub: ")
		}
		hp.URL = strings.TrimSpace(strings.TrimPrefix(l, "baton hub: "))
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for hub serve to print its URL")
	}
	return hp
}

// kill hard-kills the process (SIGKILL / TerminateProcess) and reaps it. Safe to repeat.
func (h *hubProc) kill() {
	if h.cmd.ProcessState != nil {
		return
	}
	_ = h.cmd.Process.Kill()
	done := make(chan struct{})
	go func() { _ = h.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// TestCrashRecovery kills a separate hub process mid-run; the CLI spools and a restart recovers
// everything with no loss and no duplicates.
func TestCrashRecovery(t *testing.T) {
	requireBin(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	tokenFile := filepath.Join(t.TempDir(), "admin.token")
	if err := os.WriteFile(tokenFile, []byte(crashToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hp := startUp(t, dataDir, tokenFile)
	reg := registerDev(t, hp.URL, crashToken, map[string]any{"role": "implementer", "kind": "generic"})
	env := fastEnv(reg.Env)

	for _, args := range [][]string{{"hello"}, {"progress", "10", "before crash"}, {"progress", "20", "still before"}} {
		r := baton(t, env, args, "")
		if r.Code != 0 || !strings.HasPrefix(r.Out, "ok #") {
			t.Fatalf("baton %v: %+v", args, r)
		}
	}
	if n := len(events(t, hp.URL, crashToken, reg.RunID)); n < 3 {
		t.Fatalf("only %d events before crash", n)
	}

	hp.kill()

	for _, args := range [][]string{{"progress", "30", "while down"}, {"progress", "40", "also down"}} {
		r := baton(t, env, args, "")
		if r.Code != 0 || !strings.Contains(r.Out, "spooled") {
			t.Fatalf("baton %v after kill: want spooled, got %+v", args, r)
		}
	}

	hp2 := startUp(t, dataDir, tokenFile)
	env["BATON_URL"] = hp2.URL

	want := []any{"before crash", "still before", "while down", "also down"}
	evs := until(t, 15*time.Second, "outbox recovery", func() ([]event, bool) {
		evs := events(t, hp2.URL, crashToken, reg.RunID)
		n := 0
		for _, e := range evs {
			if e.Type == "progress" {
				n++
			}
		}
		return evs, n >= len(want)
	})
	var notes []any
	seen := map[string]bool{}
	for i, e := range evs {
		if e.Seq != i+1 {
			t.Fatalf("seq at index %d = %d: want 1..n", i, e.Seq)
		}
		if seen[e.MsgID] {
			t.Fatalf("duplicate msg_id %s", e.MsgID)
		}
		seen[e.MsgID] = true
		if e.Type == "progress" {
			notes = append(notes, e.Payload["note"])
		}
	}
	if !reflect.DeepEqual(notes, want) {
		t.Fatalf("progress notes = %v, want %v", notes, want)
	}

	// the agent can keep reporting to the restarted hub
	if r := baton(t, env, []string{"progress", "50", "after restart"}, ""); !strings.HasPrefix(r.Out, "ok #") {
		t.Fatalf("post-restart progress: %+v", r)
	}
}
