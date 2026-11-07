// Package daemon finds, starts and stops the background hub of a project (design §5.5).
// The lock file (hub.lock), not a pid, decides whether a hub is running; hub.json says where.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/store"
)

// InfoFile is the discovery file in the data dir.
const InfoFile = "hub.json"

// StartWait is how long StartDetached and WaitRunning wait for a hub to come up.
const StartWait = 10 * time.Second

// Info is the content of hub.json.
type Info struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	URL       string `json:"url"`
	Instance  string `json:"instance"`
	Version   string `json:"version"`
	Exe       string `json:"exe"`
	StartedAt string `json:"started_at"`
	DataDir   string `json:"data_dir"`
	Project   string `json:"project"`
}

// State is what Discover found.
type State int

const (
	Stopped  State = iota // no hub: lock free
	Running               // hub answers /api/health with the recorded instance
	Starting              // lock held but no healthy hub.json yet
)

func (s State) String() string {
	switch s {
	case Running:
		return "running"
	case Starting:
		return "starting"
	}
	return "stopped"
}

// WriteInfo writes hub.json atomically.
func WriteInfo(dataDir string, in Info) error {
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(filepath.Join(dataDir, InfoFile), append(b, '\n'), 0o644)
}

// RemoveInfo deletes hub.json (missing is fine).
func RemoveInfo(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, InfoFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadInfo reads hub.json.
func ReadInfo(dataDir string) (*Info, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, InfoFile))
	if err != nil {
		return nil, err
	}
	var in Info
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("%s: %w", InfoFile, err)
	}
	return &in, nil
}

var client = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}

// healthy reports whether the hub described by in answers and is the same instance.
func healthy(in *Info) bool {
	if in == nil || in.URL == "" {
		return false
	}
	resp, err := client.Get(strings.TrimRight(in.URL, "/") + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		OK       bool   `json:"ok"`
		Name     string `json:"name"`
		Instance string `json:"instance"`
	}
	if json.NewDecoder(resp.Body).Decode(&h) != nil {
		return false
	}
	return h.OK && h.Name == "baton" && h.Instance != "" && h.Instance == in.Instance
}

// Discover implements §5.5: hub.json + /api/health with matching instance means Running.
// Otherwise the lock decides: free means Stopped (a stale hub.json is deleted), held means
// Starting. A single probe; it does not wait.
func Discover(dataDir string) (*Info, State, error) {
	in, err := ReadInfo(dataDir)
	if err != nil {
		in = nil // missing or corrupt hub.json: treat as absent; the lock decides
	}
	if healthy(in) {
		return in, Running, nil
	}
	held, err := LockHeld(dataDir)
	if err != nil {
		return nil, Stopped, err
	}
	if !held {
		_ = RemoveInfo(dataDir)
		return nil, Stopped, nil
	}
	return in, Starting, nil
}

// WaitRunning polls Discover until Running or timeout.
func WaitRunning(dataDir string, timeout time.Duration) (*Info, error) {
	end := time.Now().Add(timeout)
	for {
		in, st, err := Discover(dataDir)
		if err != nil {
			return nil, err
		}
		if st == Running {
			return in, nil
		}
		if time.Now().After(end) {
			return nil, fmt.Errorf("hub did not become ready within %s (state: %s); see %s", timeout, st, filepath.Join(dataDir, "logs"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// LockHeld reports whether some process holds hub.lock (it takes and releases the lock to find out).
func LockHeld(dataDir string) (bool, error) {
	if err := paths.EnsureDataDir(dataDir); err != nil {
		return false, err
	}
	l, ok, err := tryLock(filepath.Join(dataDir, hub.LockFile))
	if err != nil {
		return false, err
	}
	if ok {
		l.release()
	}
	return !ok, nil
}

// StartDetached runs `exe args...` as a background process that outlives the caller, with
// stdout/stderr going to logs/hub.out, then waits (up to 10 s) until a hub is Running.
func StartDetached(exe string, args []string, dataDir string) error {
	logDir := filepath.Join(dataDir, "logs")
	if err := paths.EnsureDataDir(dataDir); err != nil {
		return err
	}
	if err := paths.EnsurePrivateDir(logDir); err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(logDir, "hub.out"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_ = paths.Chmod(out.Name(), 0o600)
	defer out.Close()

	var cmd *exec.Cmd
	for attempt := 0; ; attempt++ {
		cmd = exec.Command(exe, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, out, out
		cmd.SysProcAttr = detachAttr(attempt == 0)
		err = cmd.Start()
		if err == nil {
			break
		}
		if attempt == 0 && retryWithoutBreakaway(err) {
			continue
		}
		return fmt.Errorf("start hub: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	end := time.Now().Add(StartWait)
	for {
		if _, st, derr := Discover(dataDir); derr == nil && st == Running {
			return nil
		}
		select {
		case <-exited:
			// The child may have lost a start race to another hub; accept that outcome.
			if _, st, derr := Discover(dataDir); derr == nil && st == Running {
				return nil
			}
			return fmt.Errorf("hub exited during start; see %s", filepath.Join(logDir, "hub.out"))
		default:
		}
		if time.Now().After(end) {
			return fmt.Errorf("hub did not become ready within %s; see %s", StartWait, logDir)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Stop asks the hub to shut down (POST /api/hub/shutdown), then waits for hub.lock to be
// released. After the timeout it kills the pid from hub.json, but only while the lock is
// still held (so a recycled pid is never killed). A hub that is not running is not an error.
func Stop(dataDir, adminToken string, timeout time.Duration) error {
	in, st, err := Discover(dataDir)
	if err != nil {
		return err
	}
	if st == Stopped {
		return nil
	}
	if in != nil && in.URL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(in.URL, "/")+"/api/hub/shutdown", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return errors.New("hub refused the admin token (check admin.token)")
			}
		}
	}
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		held, err := LockHeld(dataDir)
		if err != nil {
			return err
		}
		if !held {
			_ = RemoveInfo(dataDir)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	held, err := LockHeld(dataDir)
	if err != nil {
		return err
	}
	if !held {
		_ = RemoveInfo(dataDir)
		return nil
	}
	if in == nil || in.PID <= 0 {
		return errors.New("hub did not stop and its pid is unknown")
	}
	p, err := os.FindProcess(in.PID)
	if err != nil {
		return err
	}
	if err := p.Kill(); err != nil {
		return fmt.Errorf("kill hub pid %d: %w", in.PID, err)
	}
	for i := 0; i < 100; i++ {
		if held, _ := LockHeld(dataDir); !held {
			_ = RemoveInfo(dataDir)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("hub pid %d still holds the lock after kill", in.PID)
}
