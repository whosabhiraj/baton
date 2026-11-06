package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dEnv is a daemon test sandbox: its own BATON_HOME and git repo.
type dEnv struct {
	t    *testing.T
	env  map[string]string
	repo string
}

func newDEnv(t *testing.T) *dEnv {
	t.Helper()
	requireBin(t)
	d := &dEnv{t: t, env: map[string]string{"BATON_HOME": t.TempDir()}}
	d.repo = t.TempDir()
	if b, err := exec.Command("git", "-C", d.repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, b)
	}
	t.Cleanup(func() {
		// never leave a hub behind
		d.run(d.repo, "", "hub", "stop", "--yes")
		if in := d.info(); in != nil {
			if p, err := os.FindProcess(in.PID); err == nil {
				_ = p.Kill()
			}
		}
	})
	return d
}

// run executes baton in dir (20 s cap).
func (d *dEnv) run(dir, input string, args ...string) res {
	d.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := batonCmd(ctx, d.env, args)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if ctx.Err() != nil {
		d.t.Fatalf("baton %v timed out (stdout %q stderr %q)", args, so.String(), se.String())
	}
	return res{exitCode(err), strings.TrimSpace(so.String()), strings.TrimSpace(se.String())}
}

type hubInfo struct {
	PID      int    `json:"pid"`
	URL      string `json:"url"`
	Instance string `json:"instance"`
	Version  string `json:"version"`
}

// dataDir parses it from `baton hub status` output.
func (d *dEnv) dataDir() string {
	d.t.Helper()
	r := d.run(d.repo, "", "hub", "status")
	return field(r.Out, "data dir")
}

func field(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), name+": "); ok {
			return v
		}
	}
	return ""
}

func (d *dEnv) info() *hubInfo {
	dd := d.dataDir()
	if dd == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dd, "hub.json"))
	if err != nil {
		return nil
	}
	var in hubInfo
	if json.Unmarshal(b, &in) != nil {
		return nil
	}
	return &in
}

func (d *dEnv) mustInfo() *hubInfo {
	d.t.Helper()
	in := d.info()
	if in == nil || in.PID == 0 || in.URL == "" || in.Instance == "" {
		d.t.Fatalf("hub.json missing or incomplete: %+v", in)
	}
	return in
}

func TestDaemonLifecycle(t *testing.T) {
	d := newDEnv(t)

	r := d.run(d.repo, "", "up", "--no-browser")
	if r.Code != 0 || !strings.HasPrefix(r.Out, "baton hub: http://127.0.0.1:") || !strings.Contains(r.Out, "dashboard: http://127.0.0.1:") {
		t.Fatalf("up: %+v", r)
	}
	if strings.Contains(r.Out, "?token") {
		t.Fatal("token in URL")
	}
	in := d.mustInfo()
	if in.Version == "" {
		t.Fatal("hub.json lacks version")
	}
	token := field(r.Out, "admin token")
	if !strings.HasPrefix(token, "bt_adm_") {
		t.Fatalf("admin token line: %q", r.Out)
	}
	if st := api(t, in.URL, token, "GET", "/api/state", nil); st["ok"] != true {
		t.Fatalf("state: %v", st)
	}

	// the hub is a background process: `up` returned and it still answers; status sees it
	s := d.run(d.repo, "", "hub", "status")
	if s.Code != 0 || field(s.Out, "hub") != "running" || field(s.Out, "url") != in.URL || field(s.Out, "live runs") != "0" {
		t.Fatalf("status: %+v", s)
	}

	// second up reuses the hub; so does a subfolder
	sub := filepath.Join(d.repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{d.repo, sub} {
		if r := d.run(dir, "", "up", "--no-browser"); r.Code != 0 {
			t.Fatalf("up in %s: %+v", dir, r)
		}
		if got := d.mustInfo(); got.PID != in.PID || got.Instance != in.Instance {
			t.Fatalf("up in %s started another hub: %+v vs %+v", dir, got, in)
		}
	}

	// bare `baton` and attach
	if r := d.run(d.repo, "", "--no-browser"); r.Code != 0 || !strings.Contains(r.Out, "conductor: arrives in Phase 6") {
		t.Fatalf("baton: %+v", r)
	}
	if r := d.run(d.repo, "", "attach"); r.Code != 0 || !strings.Contains(r.Out, "conductor chat arrives in Phase 6") {
		t.Fatalf("attach: %+v", r)
	}

	// graceful stop
	dd := d.dataDir()
	if r := d.run(d.repo, "", "hub", "stop", "--yes"); r.Code != 0 {
		t.Fatalf("stop: %+v", r)
	}
	if s := d.run(d.repo, "", "hub", "status"); field(s.Out, "hub") != "stopped" {
		t.Fatalf("status after stop: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dd, "hub.json")); !os.IsNotExist(err) {
		t.Fatal("hub.json remains after stop")
	}
	if _, err := httpClient.Get(in.URL + "/api/health"); err == nil {
		t.Fatal("hub still answering after stop")
	}
}

func TestDaemonHardKillThenUp(t *testing.T) {
	d := newDEnv(t)
	if r := d.run(d.repo, "", "up", "--no-browser"); r.Code != 0 {
		t.Fatal(r)
	}
	old := d.mustInfo()
	dd := d.dataDir()
	p, err := os.FindProcess(old.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	until(t, 10*time.Second, "hub to die", func() (bool, bool) {
		_, err := httpClient.Get(old.URL + "/api/health")
		return true, err != nil
	})
	// stale hub.json is still there until someone looks
	if s := d.run(d.repo, "", "hub", "status"); field(s.Out, "hub") != "stopped" {
		t.Fatalf("status after kill: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dd, "hub.json")); !os.IsNotExist(err) {
		t.Fatal("stale hub.json not cleaned")
	}
	if r := d.run(d.repo, "", "up", "--no-browser"); r.Code != 0 {
		t.Fatalf("up after kill: %+v", r)
	}
	if n := d.mustInfo(); n.PID == old.PID || n.Instance == old.Instance {
		t.Fatalf("expected a new hub: %+v vs %+v", n, old)
	}
}

func TestDaemonNotARepo(t *testing.T) {
	d := newDEnv(t)
	plain := t.TempDir()
	// the OS temp dir can sit inside a repo (e.g. a repo at the home folder): stop git looking above it
	d.env["GIT_CEILING_DIRECTORIES"] = filepath.ToSlash(filepath.Dir(plain))
	r := d.run(plain, "n\n", "up", "--no-browser")
	if r.Code != 2 || !strings.Contains(r.Err, "is not a git repository. Initialise one here? [y/N]") {
		t.Fatalf("n: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(plain, ".git")); err == nil {
		t.Fatal("repo created on n")
	}
	// EOF = no
	if r := d.run(plain, "", "up", "--no-browser"); r.Code != 2 {
		t.Fatalf("eof: %+v", r)
	}
	r = d.run(plain, "y\n", "up", "--no-browser")
	if r.Code != 0 {
		t.Fatalf("y: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(plain, ".git")); err != nil {
		t.Fatal("repo not created on y")
	}
	t.Cleanup(func() {
		d.run(plain, "", "hub", "stop", "--yes")
	})
}

func TestDaemonRotateToken(t *testing.T) {
	d := newDEnv(t)
	r := d.run(d.repo, "", "up", "--no-browser")
	if r.Code != 0 {
		t.Fatal(r)
	}
	oldTok := field(r.Out, "admin token")
	old := d.mustInfo()

	rr := d.run(d.repo, "", "hub", "rotate-token")
	newTok := field(rr.Out, "admin token")
	if rr.Code != 0 || newTok == "" || newTok == oldTok {
		t.Fatalf("rotate: %+v (old %s)", rr, oldTok)
	}
	in := d.mustInfo()
	if in.PID == old.PID {
		t.Fatal("hub was not restarted")
	}
	if m := api(t, in.URL, newTok, "GET", "/api/state", nil); m["ok"] != true {
		t.Fatalf("new token refused: %v", m)
	}
	if m := api(t, in.URL, oldTok, "GET", "/api/state", nil); m["ok"] == true {
		t.Fatalf("old token still accepted: %v", m)
	}
}
