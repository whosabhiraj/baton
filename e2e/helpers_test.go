package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// res is the outcome of one baton invocation.
type res struct {
	Code     int
	Out, Err string
}

// baseEnv is the minimal inherited environment (so the binary can start on every OS).
func baseEnv() []string {
	var e []string
	for _, k := range []string{"PATH", "SystemRoot", "SYSTEMROOT", "TMP", "TEMP", "TMPDIR", "HOME", "USERPROFILE"} {
		if v, ok := os.LookupEnv(k); ok {
			e = append(e, k+"="+v)
		}
	}
	return e
}

func envList(env map[string]string) []string {
	out := baseEnv()
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// batonCmd builds (but does not start) a baton command with exactly the given env.
func batonCmd(ctx context.Context, env map[string]string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Env = envList(env)
	return cmd
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

// baton runs the binary to completion (20 s cap) and returns trimmed output.
func baton(t *testing.T, env map[string]string, args []string, input string) res {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := batonCmd(ctx, env, args)
	cmd.Stdin = strings.NewReader(input)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("baton %v timed out", args)
	}
	code := exitCode(err)
	if code < 0 {
		t.Fatalf("running baton %v: %v", args, err)
	}
	return res{code, strings.TrimSpace(so.String()), strings.TrimSpace(se.String())}
}

// batonAsync starts baton in the background; the result arrives on the channel.
func batonAsync(env map[string]string, args []string, timeout time.Duration) <-chan res {
	ch := make(chan res, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := batonCmd(ctx, env, args)
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		err := cmd.Run()
		ch <- res{exitCode(err), strings.TrimSpace(so.String()), strings.TrimSpace(se.String())}
	}()
	return ch
}

// waitRes waits for an async baton result, failing the test after d.
func waitRes(t *testing.T, ch <-chan res, d time.Duration) res {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(d):
		t.Fatal("timed out waiting for baton to exit")
		return res{}
	}
}

// until polls fn every 50 ms until it returns ok, or fails the test after timeout.
func until[T any](t *testing.T, timeout time.Duration, what string, fn func() (T, bool)) T {
	t.Helper()
	end := time.Now().Add(timeout)
	for {
		if v, ok := fn(); ok {
			return v
		}
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

var httpClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}

// api calls the hub with the admin token and decodes the JSON reply.
func api(t *testing.T, url, token, method, path string, body any) map[string]any {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("%s %s: decode: %v", method, path, err)
	}
	return m
}

// event is the subset of a stored event the tests look at.
type event struct {
	Seq     int            `json:"seq"`
	MsgID   string         `json:"msg_id"`
	Type    string         `json:"type"`
	Src     string         `json:"src"`
	Payload map[string]any `json:"payload"`
}

func events(t *testing.T, url, token, runID string) []event {
	t.Helper()
	m := api(t, url, token, "GET", "/api/runs/"+runID+"/events?since=0", nil)
	b, _ := json.Marshal(m["events"])
	var evs []event
	if err := json.Unmarshal(b, &evs); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	return evs
}

// runSummary returns the /api/state entry for runID.
func runSummary(t *testing.T, url, token, runID string) map[string]any {
	t.Helper()
	st := api(t, url, token, "GET", "/api/state", nil)
	runs, _ := st["runs"].([]any)
	for _, r := range runs {
		if rm, ok := r.(map[string]any); ok && rm["run_id"] == runID {
			return rm
		}
	}
	t.Fatalf("run %s not in /api/state", runID)
	return nil
}

// registered is the reply of POST /api/dev/runs.
type registered struct {
	RunID, RunDir string
	Env           map[string]string
}

func registerDev(t *testing.T, url, token string, body map[string]any) registered {
	t.Helper()
	m := api(t, url, token, "POST", "/api/dev/runs", body)
	if m["ok"] != true {
		t.Fatalf("dev run registration failed: %v", m)
	}
	r := registered{RunID: m["run_id"].(string), RunDir: m["run_dir"].(string), Env: map[string]string{}}
	for k, v := range m["env"].(map[string]any) {
		r.Env[k] = v.(string)
	}
	return r
}

// readJSONL parses a JSONL file into maps; a missing file means no lines.
func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad jsonl line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// fastEnv adds the quick poll/heartbeat intervals the lifecycle tests use.
func fastEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		out[k] = v
	}
	out["BATON_POLL_INTERVAL_S"] = "0.1"
	out["BATON_HEARTBEAT_INTERVAL_S"] = "0.3"
	return out
}
