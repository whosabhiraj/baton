// Package cli implements the `baton` command: what agents call to report to, and hear from,
// the hub (PROTOCOL.md §3.4, A1-A12), plus the few human commands of Phase 1.
// Output lines and exit codes are part of the protocol (A9); e2e/cli_test.go pins them.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Version is set at build time via -ldflags "-X github.com/whosabhiraj/baton/internal/cli.Version=...".
var Version = "dev"

const usage = `usage: baton <command>
  hello | plan [--file f | --confirm] | step <id> start|done|blocked [--note|--summary|--evidence|--reason]
  progress <0-100> "note" | artifact <file|diff|commit|url|log> <ref> [--note]
  ask "question" [--options a,b] [--blocking] [--timeout s] | inbox | heartbeat
  done --status success|partial|failed --summary ".." [--followups ".."] | error "msg" [--fatal]
  up [--port N] [--dev]   (foreground hub for manual testing)`

type ctx struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string
	env            *runEnv
	client         *http.Client
}

var maxPositionals = map[string]int{
	"hello": 0, "heartbeat": 0, "inbox": 0, "plan": 0, "done": 0, "step": 2, "progress": 2, "artifact": 2, "ask": 1, "error": 1,
}

var laterPhase = map[string]int{"attach": 2, "hub": 2, "mock": 3, "login": 5, "gc": 7}

// Main runs the CLI and returns the process exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	c := &ctx{stdin: stdin, stdout: stdout, stderr: stderr, getenv: getenv, client: httpClient()}
	code, err := c.dispatch(args)
	if err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "baton: %s. Try: baton help\n", ue.msg)
			return 2
		}
		fmt.Fprintf(stderr, "baton: %s\n", err.Error())
		return 1
	}
	return code
}

func need(v string, what string) (string, error) {
	if v == "" {
		return "", usagef("missing %s", what)
	}
	return v, nil
}

func (c *ctx) dispatch(argv []string) (int, error) {
	o, pos, err := parseArgs(argv)
	if err != nil {
		return 0, usagef("%s", err.Error())
	}
	var cmd string
	if len(pos) > 0 {
		cmd, pos = pos[0], pos[1:]
	}
	if cmd == "" && !o.flag("help") {
		return 0, usagef("missing command")
	}
	if cmd == "" || cmd == "help" || o.flag("help") {
		fmt.Fprintln(c.stdout, usage)
		return 0, nil
	}
	switch cmd {
	case "version":
		fmt.Fprintf(c.stdout, "baton %s\n", Version)
		return 0, nil
	case "up":
		return c.cmdUp(o)
	}
	if ph, ok := laterPhase[cmd]; ok {
		fmt.Fprintf(c.stderr, "baton: %s not available yet (Phase %d)\n", cmd, ph)
		return 2, nil
	}
	max, ok := maxPositionals[cmd]
	if !ok {
		return 0, usagef("unknown command %q", cmd)
	}
	if len(pos) > max {
		return 0, usagef("unexpected argument %q", pos[max])
	}
	if c.env, err = loadRunEnv(c.getenv); err != nil {
		return 0, err
	}
	arg := func(i int) string {
		if i < len(pos) {
			return pos[i]
		}
		return ""
	}
	// optional string flag -> payload value only when defined
	set := func(p payload, key, flag string) {
		if o.has(flag) {
			p[key] = o.get(flag)
		}
	}
	var p payload
	switch cmd {
	case "hello":
		cwd, _ := os.Getwd()
		p = payload{"cwd": cwd, "brief_ack": true}
		if m := c.getenv("BATON_MODEL"); m != "" {
			p["model"] = m
		}
		return c.send("hello", p, false)
	case "heartbeat":
		return c.send("heartbeat", payload{}, false)
	case "inbox":
		return c.cmdInbox(), nil
	case "plan":
		if o.flag("confirm") {
			return c.send("plan", payload{"confirm": true}, false)
		}
		pl, err := c.readPlan(o)
		if err != nil {
			return 0, err
		}
		return c.send("plan", pl, false)
	case "step":
		id, err := need(arg(0), "step id")
		if err != nil {
			return 0, err
		}
		action, err := need(arg(1), "action (start|done|blocked)")
		if err != nil {
			return 0, err
		}
		p = payload{"step_id": id}
		switch action {
		case "start":
			set(p, "note", "note")
			return c.send("step_start", p, false)
		case "done":
			s, err := need(o.get("summary"), "--summary")
			if err != nil {
				return 0, err
			}
			p["summary"] = s
			set(p, "evidence", "evidence")
			return c.send("step_done", p, false)
		case "blocked":
			r, err := need(o.get("reason"), "--reason")
			if err != nil {
				return 0, err
			}
			p["reason"] = r
			return c.send("step_blocked", p, false)
		}
		return 0, usagef("unknown step action %q (start|done|blocked)", action)
	case "progress":
		s, err := need(arg(0), "percent (0-100)")
		if err != nil {
			return 0, err
		}
		f := -1.0
		if percentRe.MatchString(s) {
			f = num(s, -1)
		}
		if f < 0 || f > 100 {
			return 0, usagef("percent must be 0-100, got %q", s)
		}
		return c.send("progress", payload{"percent": f, "note": arg(1)}, false)
	case "artifact":
		k, err := need(arg(0), "kind (file|diff|commit|url|log)")
		if err != nil {
			return 0, err
		}
		r, err := need(arg(1), "ref")
		if err != nil {
			return 0, err
		}
		p = payload{"kind": k, "ref": r}
		set(p, "note", "note")
		return c.send("artifact", p, false)
	case "ask":
		q, err := need(arg(0), "question text")
		if err != nil {
			return 0, err
		}
		return c.cmdAsk(q, o)
	case "done":
		st, err := need(o.get("status"), "--status success|partial|failed")
		if err != nil {
			return 0, err
		}
		sm, err := need(o.get("summary"), "--summary")
		if err != nil {
			return 0, err
		}
		p = payload{"status": st, "summary": sm}
		set(p, "followups", "followups")
		return c.send("done", p, false)
	}
	m, err := need(arg(0), "error message")
	if err != nil {
		return 0, err
	}
	return c.send("error", payload{"message": m, "fatal": o.flag("fatal")}, false)
}

var percentRe = regexp.MustCompile(`^\d+(\.\d+)?$`)

// readPlan loads the plan from --file or stdin (capped at 10 s so a stuck pipe cannot hang the agent).
func (c *ctx) readPlan(o opts) (payload, error) {
	var raw []byte
	if o.has("file") {
		b, err := os.ReadFile(o.get("file"))
		if err != nil {
			return nil, usagef("cannot read plan: %s", err.Error())
		}
		raw = b
	} else {
		if f, ok := c.stdin.(*os.File); ok {
			if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
				return nil, usagef("plan needs --file <plan.json>, piped JSON on stdin, or --confirm")
			}
		}
		type res struct {
			b   []byte
			err error
		}
		ch := make(chan res, 1)
		go func() { b, err := io.ReadAll(c.stdin); ch <- res{b, err} }()
		select {
		case r := <-ch:
			if r.err != nil {
				return nil, usagef("cannot read plan: %s", r.err.Error())
			}
			raw = r.b
		case <-time.After(planStdinTimeout):
			return nil, usagef("cannot read plan: timed out reading stdin")
		}
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var j any
	if err := d.Decode(&j); err != nil || d.More() || strings.TrimSpace(string(raw)) == "" {
		return nil, usagef("plan is not valid JSON")
	}
	switch t := j.(type) {
	case []any:
		return payload{"steps": t}, nil
	case map[string]any:
		return t, nil
	}
	return nil, usagef("invalid plan: must be a JSON object or array of steps")
}

var planStdinTimeout = 10 * time.Second
