// Package codexapp is `baton __codex`: the driver for sandboxed codex agents. It runs
// `codex app-server` (JSON-RPC over stdio, one JSON object per line), starts one thread and one
// turn with the brief from stdin, writes the app-server's notifications to stdout as JSONL for
// the codex parser, turns the app-server's approval requests into Baton access requests (the
// user decides in the dashboard, PROTOCOL.md A20), and exits when the turn completes.
package codexapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/adapters"
)

// Env names the launcher sets for the driver only (the codex process never gets them).
const (
	EnvURL   = "BATON_DRIVER_URL"
	EnvToken = "BATON_DRIVER_TOKEN"
	EnvCLI   = "BATON_CODEX_CLI" // tests: run something else instead of `codex`
)

// Options for Run; zero values come from the environment.
type Options struct {
	Args       []string // extra codex arguments (-c overrides), placed after `app-server`
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Getenv     func(string) string
	AskTimeout time.Duration
	Version    string
}

type driver struct {
	o      Options
	hub    *hubClient
	in     io.WriteCloser // codex stdin
	inMu   sync.Mutex
	outMu  sync.Mutex
	nextID int
	waits  map[int]chan rpcMsg
	waitMu sync.Mutex
	turnID string
	done   chan int // exit code once the turn completed
}

type rpcMsg struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Run drives one codex turn and returns the process exit code.
func Run(o Options) int {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.AskTimeout <= 0 {
		o.AskTimeout = 900 * time.Second
		if s, err := strconv.ParseFloat(o.Getenv("BATON_ASK_TIMEOUT_S"), 64); err == nil && s > 0 {
			o.AskTimeout = time.Duration(s * float64(time.Second))
		}
	}
	d := &driver{o: o, waits: map[int]chan rpcMsg{}, done: make(chan int, 1),
		hub: &hubClient{url: strings.TrimRight(o.Getenv(EnvURL), "/"), token: o.Getenv(EnvToken), run: o.Getenv("BATON_RUN_ID")}}
	code, err := d.run()
	if err != nil {
		fmt.Fprintf(o.Stderr, "baton __codex: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func (d *driver) run() (int, error) {
	brief, err := io.ReadAll(d.o.Stdin)
	if err != nil {
		return 1, fmt.Errorf("read brief: %w", err)
	}
	cli := d.o.Getenv(EnvCLI)
	if cli == "" {
		cli = "codex"
	}
	path, err := exec.LookPath(cli)
	if err != nil {
		return 1, fmt.Errorf("%s not found on PATH", cli)
	}
	cmd := exec.Command(path, append([]string{"app-server"}, d.o.Args...)...)
	cmd.Env = childEnv(os.Environ())
	cmd.Stderr = d.o.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return 1, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start codex app-server: %w", err)
	}
	d.in = in
	exited := make(chan error, 1)
	readDone := make(chan struct{})
	go func() { d.read(out); close(readDone) }()
	go func() { <-readDone; exited <- cmd.Wait() }()
	stop := func() {
		_ = in.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

	setup := make(chan error, 1)
	go func() { setup <- d.start(string(brief)) }()
	for {
		select {
		case err := <-setup:
			if err != nil {
				stop()
				return 1, err
			}
			setup = nil
		case code := <-d.done:
			stop() // the turn is over; app-server would otherwise keep running
			return code, nil
		case err := <-exited:
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode(), errors.New("codex app-server exited before the turn completed")
			}
			if err != nil {
				return 1, err
			}
			return 1, errors.New("codex app-server exited before the turn completed")
		}
	}
}

// childEnv drops the driver-only variables (and any hub credentials) from codex's environment,
// so commands codex runs in the sandbox never see them.
func childEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case EnvURL, EnvToken, "BATON_URL", "BATON_TOKEN", EnvCLI:
			continue
		}
		out = append(out, kv)
	}
	return out
}

// start: initialize, initialized, thread/start, turn/start.
func (d *driver) start(brief string) error {
	if _, err := d.call("initialize", map[string]any{"clientInfo": map[string]any{"name": "baton", "title": "Baton", "version": d.o.Version}}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := d.send(map[string]any{"method": "initialized"}); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	res, err := d.call("thread/start", map[string]any{"cwd": cwd, "approvalPolicy": "on-request", "approvalsReviewer": "user"})
	if err != nil {
		return fmt.Errorf("thread/start: %w", err)
	}
	var ts struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(res, &ts) != nil || ts.Thread.ID == "" {
		return fmt.Errorf("thread/start: no thread id in %s", trunc(string(res), 200))
	}
	res, err = d.call("turn/start", map[string]any{"threadId": ts.Thread.ID,
		"input": []any{map[string]any{"type": "text", "text": brief}}})
	if err != nil {
		return fmt.Errorf("turn/start: %w", err)
	}
	var tr struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(res, &tr)
	d.waitMu.Lock()
	d.turnID = tr.Turn.ID
	d.waitMu.Unlock()
	return nil
}

func (d *driver) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	d.inMu.Lock()
	defer d.inMu.Unlock()
	_, err = d.in.Write(append(b, '\n'))
	return err
}

func (d *driver) call(method string, params any) (json.RawMessage, error) {
	d.waitMu.Lock()
	d.nextID++
	id := d.nextID
	ch := make(chan rpcMsg, 1)
	d.waits[id] = ch
	d.waitMu.Unlock()
	if err := d.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("%s (code %d)", m.Error.Message, m.Error.Code)
		}
		return m.Result, nil
	case <-time.After(2 * time.Minute):
		return nil, errors.New("no answer from codex app-server")
	}
}

// emit writes one line for the parser.
func (d *driver) emit(line []byte) {
	d.outMu.Lock()
	defer d.outMu.Unlock()
	_, _ = d.o.Stdout.Write(append(bytes.TrimSpace(line), '\n'))
}

func (d *driver) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var m rpcMsg
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && m.ID != nil: // a request from codex
			go d.serve(m)
		case m.Method != "": // a notification
			d.emit(line)
			if m.Method == "turn/completed" {
				d.turnCompleted(m.Params)
			}
		case m.ID != nil: // a response to one of our calls
			var id int
			if json.Unmarshal(*m.ID, &id) == nil {
				d.waitMu.Lock()
				ch := d.waits[id]
				delete(d.waits, id)
				d.waitMu.Unlock()
				if ch != nil {
					ch <- m
				}
			}
		}
	}
}

func (d *driver) turnCompleted(params json.RawMessage) {
	var p struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &p)
	d.waitMu.Lock()
	ours := d.turnID == "" || p.Turn.ID == d.turnID
	d.waitMu.Unlock()
	if !ours {
		return
	}
	code := 0
	if p.Turn.Status != "completed" {
		code = 1
	}
	select {
	case d.done <- code:
	default:
	}
}

// serve answers one request from codex.
func (d *driver) serve(m rpcMsg) {
	reply := func(result any) { _ = d.send(map[string]any{"id": m.ID, "result": result}) }
	var p map[string]any
	_ = json.Unmarshal(m.Params, &p)
	str := func(k string) string { s, _ := p[k].(string); return s }
	switch m.Method {
	case "item/commandExecution/requestApproval":
		detail := map[string]any{"command": adapters.UnwrapShell(str("command")), "cwd": str("cwd")}
		if nc, ok := p["networkApprovalContext"].(map[string]any); ok {
			for _, k := range []string{"host", "url", "domain"} {
				if s, ok := nc[k].(string); ok && s != "" {
					detail["network_host"] = s
				}
			}
		}
		summary := str("reason")
		if summary == "" {
			summary = "run outside the sandbox: " + str("command")
		}
		if d.ask("command", summary, detail) {
			reply(map[string]any{"decision": "accept"})
		} else {
			reply(map[string]any{"decision": "decline"})
		}
	case "item/fileChange/requestApproval":
		summary := str("reason")
		if summary == "" {
			summary = "change files outside its area"
		}
		detail := map[string]any{"reason": str("reason"), "paths": str("grantRoot")}
		if d.ask("file_change", summary, detail) {
			reply(map[string]any{"decision": "accept"})
		} else {
			reply(map[string]any{"decision": "decline"})
		}
	case "item/permissions/requestApproval":
		perms, _ := p["permissions"].(map[string]any)
		summary := str("reason")
		if summary == "" {
			summary = describePermissions(perms)
		}
		detail := map[string]any{"reason": str("reason"), "cwd": str("cwd"), "permissions": describePermissions(perms)}
		if d.ask("permissions", summary, detail) {
			reply(map[string]any{"permissions": perms, "scope": "turn"})
		} else {
			reply(map[string]any{"permissions": map[string]any{}, "scope": "turn"})
		}
	case "execCommandApproval", "applyPatchApproval": // legacy shapes
		kind, summary := "command", str("reason")
		if m.Method == "applyPatchApproval" {
			kind = "file_change"
		}
		if summary == "" {
			summary = m.Method
		}
		if d.ask(kind, summary, map[string]any{"reason": str("reason"), "cwd": str("cwd")}) {
			reply(map[string]any{"decision": "approved"})
		} else {
			reply(map[string]any{"decision": "denied"})
		}
	default:
		_ = d.send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "not supported by Baton: " + m.Method}})
	}
}

// ask files an access request with the hub and waits for the user's decision. Anything but an
// approval (rejected, timed out, hub unreachable) is a no.
func (d *driver) ask(kind, summary string, detail map[string]any) bool {
	summary = trunc(summary, 300)
	if r, _ := detail["reason"].(string); r == "" || r == summary {
		delete(detail, "reason") // the summary already says it
	}
	note := func(status string) {
		b, _ := json.Marshal(map[string]any{"method": "baton/access", "params": map[string]any{"kind": kind, "summary": summary, "status": status}})
		d.emit(b)
	}
	note("asked")
	ctx, cancel := context.WithTimeout(context.Background(), d.o.AskTimeout)
	defer cancel()
	id, err := d.hub.create(ctx, kind, summary, detail)
	if err != nil {
		fmt.Fprintf(d.o.Stderr, "baton __codex: access request failed: %v\n", err)
		note("failed")
		return false
	}
	status := d.hub.wait(ctx, id)
	if status == "pending" { // timed out: withdraw it so it does not linger in the dashboard
		c2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
		d.hub.cancel(c2, id)
		cancel2()
		status = "expired"
	}
	note(status)
	return status == "approved"
}

func describePermissions(p map[string]any) string {
	var parts []string
	if n, ok := p["network"].(map[string]any); ok && n["enabled"] == true {
		parts = append(parts, "network access")
	}
	if fs, ok := p["fileSystem"].(map[string]any); ok {
		for _, k := range []string{"read", "write"} {
			if l, ok := fs[k].([]any); ok && len(l) > 0 {
				parts = append(parts, fmt.Sprintf("%s %v", k, l))
			}
		}
		if es, ok := fs["entries"].([]any); ok && len(es) > 0 {
			b, _ := json.Marshal(es)
			parts = append(parts, "paths "+trunc(string(b), 200))
		}
	}
	if len(parts) == 0 {
		return "extra permissions"
	}
	return strings.Join(parts, ", ")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// ---- hub ----

type hubClient struct {
	url, token, run string
	http            http.Client
}

func (h *hubClient) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	if h.url == "" || h.token == "" || h.run == "" {
		return 0, errors.New("no hub URL or token for access requests")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.url+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return resp.StatusCode, fmt.Errorf("hub answered %d: %s", resp.StatusCode, trunc(string(b), 200))
	}
	if out != nil {
		return 200, json.Unmarshal(b, out)
	}
	return 200, nil
}

func (h *hubClient) create(ctx context.Context, kind, summary string, detail map[string]any) (string, error) {
	var r struct {
		Approval struct {
			ID string `json:"id"`
		} `json:"approval"`
	}
	if _, err := h.do(ctx, "POST", "/api/runs/"+h.run+"/access", map[string]any{"kind": kind, "summary": summary, "detail": detail}, &r); err != nil {
		return "", err
	}
	if r.Approval.ID == "" {
		return "", errors.New("hub returned no approval id")
	}
	return r.Approval.ID, nil
}

// wait long-polls until decided or ctx ends ("pending" then). Network errors are retried.
func (h *hubClient) wait(ctx context.Context, id string) string {
	for ctx.Err() == nil {
		var r struct {
			Status string `json:"status"`
		}
		code, err := h.do(ctx, "GET", "/api/runs/"+h.run+"/access/"+id+"?wait=55", nil, &r)
		switch {
		case err == nil && r.Status != "" && r.Status != "pending":
			return r.Status
		case code >= 400 && code < 500:
			return "rejected" // gone or not ours: treat as a no
		case err != nil:
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
	return "pending"
}

func (h *hubClient) cancel(ctx context.Context, id string) {
	_, _ = h.do(ctx, "POST", "/api/runs/"+h.run+"/access/"+id+"/cancel", map[string]any{}, nil)
}
