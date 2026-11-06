// Package mcpserver is `baton mcp`: the conductor's MCP stdio server (GO_HUB_DESIGN §6.5,
// PROTOCOL.md A16). Every tool is a thin call to the hub's REST API with the conductor token,
// so the conductor goes through the same checks, approval gates and audit log as the dashboard.
// The only state kept here is the wait_for_events cursor.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client talks to the hub.
type Client struct {
	// BaseURL returns the hub URL; it is called again after a connection error (the hub may
	// have restarted on another port).
	BaseURL func() (string, error)
	Token   string
	HTTP    *http.Client // nil = default (no proxy, no overall timeout: wait long-polls)

	mu       sync.Mutex
	url      string
	cursor   uint64
	instance string // hub instance the cursor belongs to
	started  bool
}

// HubError is a non-2xx hub answer.
type HubError struct {
	Status int
	Code   string
	Msg    string
	Body   json.RawMessage
}

func (e *HubError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("hub answered %d: %s", e.Status, strings.TrimSpace(string(e.Body)))
	}
	return e.Code + ": " + e.Msg
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Transport: &http.Transport{Proxy: nil}}
}

func (c *Client) base(refresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.url == "" || refresh {
		u, err := c.BaseURL()
		if err != nil {
			return "", err
		}
		c.url = strings.TrimRight(u, "/")
	}
	return c.url, nil
}

// Do sends a request and returns the raw JSON body of a 2xx answer.
func (c *Client) Do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	for attempt := 0; ; attempt++ {
		base, err := c.base(attempt > 0)
		if err != nil {
			return nil, fmt.Errorf("cannot find the hub: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.httpClient().Do(req)
		if err != nil {
			if attempt == 0 && ctx.Err() == nil {
				continue // rediscover once
			}
			return nil, fmt.Errorf("hub unreachable: %w", err)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			he := &HubError{Status: resp.StatusCode, Body: b}
			var e struct {
				Code  string   `json:"code"`
				Error string   `json:"error"`
				Iss   []string `json:"issues"`
			}
			if json.Unmarshal(b, &e) == nil {
				he.Code, he.Msg = e.Code, e.Error
				if len(e.Iss) > 0 {
					he.Msg += " (" + strings.Join(e.Iss, "; ") + ")"
				}
			}
			return nil, he
		}
		return b, nil
	}
}

// startCursor pins the wait cursor to "now" the first time any tool runs, so events that
// arrive between a spawn and the first wait_for_events are not missed.
func (c *Client) startCursor(ctx context.Context) {
	c.mu.Lock()
	started := c.started
	c.mu.Unlock()
	if started {
		return
	}
	b, err := c.Do(ctx, "GET", "/api/wait?timeout_s=0&types=-", nil)
	if err != nil {
		return // try again on the next call
	}
	var w waitResp
	if json.Unmarshal(b, &w) == nil {
		c.mu.Lock()
		if !c.started {
			c.cursor, c.instance, c.started = w.Cursor, w.Instance, true
		}
		c.mu.Unlock()
	}
}

type waitResp struct {
	OK       bool              `json:"ok"`
	Cursor   uint64            `json:"cursor"`
	Instance string            `json:"instance"`
	Items    []json.RawMessage `json:"items"`
	TimedOut bool              `json:"timed_out"`
	Reset    bool              `json:"reset"`
}

// Wait long-polls GET /api/wait from the remembered cursor and advances it.
func (c *Client) Wait(ctx context.Context, timeoutS float64, types []string) (json.RawMessage, error) {
	c.startCursor(ctx)
	c.mu.Lock()
	cur, inst, started := c.cursor, c.instance, c.started
	c.mu.Unlock()
	q := url.Values{}
	q.Set("timeout_s", strconv.FormatFloat(timeoutS, 'f', -1, 64))
	if started {
		q.Set("cursor", strconv.FormatUint(cur, 10))
		q.Set("instance", inst)
	}
	if len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}
	b, err := c.Do(ctx, "GET", "/api/wait?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var w waitResp
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cursor, c.instance, c.started = w.Cursor, w.Instance, true
	c.mu.Unlock()
	out := map[string]any{"items": w.Items, "timed_out": w.TimedOut}
	if w.Items == nil {
		out["items"] = []any{}
	}
	if w.Reset {
		out["reset"] = true
		out["note"] = "The hub restarted or events were missed: call list_agents to re-read the current state."
	}
	return json.Marshal(out)
}

// ---- tool inputs ----

type empty struct{}

type agentRef struct {
	AgentID string `json:"agent_id" jsonschema:"agent id (e.g. gn-claude-a-0001) or run id"`
}

type planStep struct {
	ID         string `json:"id" jsonschema:"short step id, e.g. s1"`
	Title      string `json:"title" jsonschema:"what to do in this step"`
	Acceptance string `json:"acceptance" jsonschema:"how to check the step is done: something that can be run or inspected"`
}

type scope struct {
	Paths     []string `json:"paths,omitempty" jsonschema:"paths the agent may change (globs)"`
	Forbidden []string `json:"forbidden,omitempty" jsonschema:"paths the agent must not touch"`
	AskBefore []string `json:"ask_before,omitempty" jsonschema:"actions the agent must ask about first"`
}

type spawnIn struct {
	Account    string     `json:"account" jsonschema:"account id to run the agent on (always explicit; see list_accounts)"`
	Role       string     `json:"role" jsonschema:"role, must be in the account's roles"`
	Task       string     `json:"task" jsonschema:"the agent's mission, specific to this agent"`
	Plan       []planStep `json:"plan" jsonschema:"personalised plan: concrete steps, each with a checkable acceptance criterion"`
	Model      string     `json:"model,omitempty" jsonschema:"model, must be in the account's models; empty = account default"`
	Effort     string     `json:"effort,omitempty" jsonschema:"reasoning effort, e.g. low, medium, high"`
	Kind       string     `json:"kind,omitempty" jsonschema:"runtime kind; empty = the account's kind"`
	Callsign   string     `json:"callsign,omitempty" jsonschema:"custom callsign; empty = automatic"`
	Scope      *scope     `json:"scope,omitempty" jsonschema:"what the agent may touch"`
	BaseBranch string     `json:"base_branch,omitempty" jsonschema:"branch to start from; empty = the project's current branch"`
	DryRun     bool       `json:"dry_run,omitempty" jsonschema:"only validate and render the brief, spawn nothing"`
}

type listAgentsIn struct {
	Status     []string `json:"status,omitempty" jsonschema:"only these statuses (running, waiting, blocked, paused, stalled, rate_limited, done, failed, killed, exited)"`
	ActiveOnly bool     `json:"active_only,omitempty" jsonschema:"only agents that have not ended"`
}

type getAgentIn struct {
	AgentID string `json:"agent_id" jsonschema:"agent id or run id"`
	Events  *int   `json:"events,omitempty" jsonschema:"how many recent events to include (default 10, max 500)"`
}

type readEventsIn struct {
	AgentID  string   `json:"agent_id" jsonschema:"agent id or run id"`
	SinceSeq int64    `json:"since_seq,omitempty" jsonschema:"only events with seq greater than this"`
	Types    []string `json:"types,omitempty" jsonschema:"only these event types"`
	Limit    *int     `json:"limit,omitempty" jsonschema:"newest N events after filtering (default 50)"`
}

type waitIn struct {
	TimeoutS *float64 `json:"timeout_s,omitempty" jsonschema:"seconds to wait (default 60, max 600)"`
	Types    []string `json:"types,omitempty" jsonschema:"override what wakes you; default: question, step_blocked, done, error, stalled, rate_limited, exit, approval, merged"`
}

type messageIn struct {
	AgentID string `json:"agent_id" jsonschema:"agent id or run id"`
	Text    string `json:"text" jsonschema:"message for the agent"`
}

type answerIn struct {
	AgentID string `json:"agent_id" jsonschema:"agent id or run id"`
	QID     string `json:"qid" jsonschema:"id of the agent's open question"`
	Text    string `json:"text" jsonschema:"the answer"`
}

type planIn struct {
	AgentID string     `json:"agent_id" jsonschema:"agent id or run id"`
	Steps   []planStep `json:"steps" jsonschema:"the full new plan"`
	Reason  string     `json:"reason,omitempty" jsonschema:"why the plan changes"`
}

type controlIn struct {
	AgentID string `json:"agent_id" jsonschema:"agent id or run id"`
	Reason  string `json:"reason,omitempty" jsonschema:"why (shown to the agent and the user)"`
}

type diffIn struct {
	AgentID  string `json:"agent_id" jsonschema:"agent id or run id"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"cap on the diff text (default 200000)"`
}

type mergeIn struct {
	AgentID string `json:"agent_id" jsonschema:"agent id or run id"`
	Confirm bool   `json:"confirm" jsonschema:"must be true: you reviewed the diff with review_diff and the tests pass"`
	Note    string `json:"note,omitempty" jsonschema:"short review summary shown to the user with the merge request"`
}

type modeIn struct {
	Mode string `json:"mode" jsonschema:"user, propose or auto; only when the user asked for it"`
}

type accountEnabledIn struct {
	Account string `json:"account" jsonschema:"account id"`
	Enabled bool   `json:"enabled" jsonschema:"true = may run agents"`
}

func id(s string) string { return url.PathEscape(s) }

// textResult returns the hub's JSON as the tool's text content.
func textResult(b json.RawMessage, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		var he *HubError
		if errors.As(err, &he) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: he.Error()}}}, nil, nil
		}
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(bytes.TrimSpace(b))}}}, nil, nil
}

func tool[In any](s *mcp.Server, c *Client, name, desc string, call func(ctx context.Context, in In) (json.RawMessage, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		if name != "wait_for_events" {
			c.startCursor(ctx)
		}
		return textResult(call(ctx, in))
	})
}

// NewServer registers the conductor tools.
func NewServer(c *Client, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "baton", Version: version}, &mcp.ServerOptions{
		Instructions: "Baton hub tools: spawn and steer coding agents, review their diffs, request merges. " +
			"Always pass account explicitly when spawning. Merges need the user's approval.",
	})
	tool(s, c, "list_accounts", "Accounts with kind, roles, models, enabled flag, concurrency used/max, rate limits and usage totals.",
		func(ctx context.Context, _ empty) (json.RawMessage, error) {
			return c.Do(ctx, "GET", "/api/accounts", nil)
		})
	tool(s, c, "spawn_agent", "Spawn one agent in its own git worktree and branch. Returns agent_id, callsign and run_id, or an error such as ACCOUNT_BUSY, ROLE_NOT_ALLOWED, MODEL_NOT_ALLOWED, ACCOUNT_DISABLED.",
		func(ctx context.Context, in spawnIn) (json.RawMessage, error) {
			if strings.TrimSpace(in.Account) == "" {
				return nil, &HubError{Status: 400, Code: "ACCOUNT_REQUIRED", Msg: "pass account explicitly"}
			}
			return c.Do(ctx, "POST", "/api/spawn", in)
		})
	tool(s, c, "list_agents", "Agents (live and finished) with status, plan, step states, open questions, usage and merged flag.",
		func(ctx context.Context, in listAgentsIn) (json.RawMessage, error) {
			q := url.Values{}
			if len(in.Status) > 0 {
				q.Set("status", strings.Join(in.Status, ","))
			}
			if in.ActiveOnly {
				q.Set("active", "true")
			}
			return c.Do(ctx, "GET", "/api/runs?"+q.Encode(), nil)
		})
	tool(s, c, "get_agent", "One agent: summary, task, recent events (no activity noise), artifacts, diff stat, pending approvals.",
		func(ctx context.Context, in getAgentIn) (json.RawMessage, error) {
			p := "/api/runs/" + id(in.AgentID)
			if in.Events != nil {
				p += "?events=" + strconv.Itoa(*in.Events)
			}
			return c.Do(ctx, "GET", p, nil)
		})
	tool(s, c, "read_events", "A slice of one agent's event log. Prefer get_agent; use this for details.",
		func(ctx context.Context, in readEventsIn) (json.RawMessage, error) {
			q := url.Values{}
			q.Set("since", strconv.FormatInt(in.SinceSeq, 10))
			limit := 50
			if in.Limit != nil {
				limit = *in.Limit
			}
			q.Set("limit", strconv.Itoa(limit))
			if len(in.Types) > 0 {
				q.Set("types", strings.Join(in.Types, ","))
			}
			return c.Do(ctx, "GET", "/api/runs/"+id(in.AgentID)+"/events?"+q.Encode(), nil)
		})
	tool(s, c, "wait_for_events", "Block until something needs you: a question, a blocked step, done, error, stalled, rate limit, exit, a decided approval or a merge. Returns the new items since your last call (none are skipped).",
		func(ctx context.Context, in waitIn) (json.RawMessage, error) {
			t := 60.0
			if in.TimeoutS != nil {
				t = *in.TimeoutS
			}
			return c.Wait(ctx, t, in.Types)
		})
	tool(s, c, "send_message", "Send the agent a message (it reads it with `baton inbox`).",
		func(ctx context.Context, in messageIn) (json.RawMessage, error) {
			return c.Do(ctx, "POST", "/api/runs/"+id(in.AgentID)+"/message", map[string]any{"text": in.Text})
		})
	tool(s, c, "answer_question", "Answer one of the agent's open questions (qid from list_agents/get_agent).",
		func(ctx context.Context, in answerIn) (json.RawMessage, error) {
			return c.Do(ctx, "POST", "/api/runs/"+id(in.AgentID)+"/answer", map[string]any{"qid": in.QID, "text": in.Text})
		})
	tool(s, c, "update_plan", "Replace the agent's plan.",
		func(ctx context.Context, in planIn) (json.RawMessage, error) {
			body := map[string]any{"steps": in.Steps}
			if in.Reason != "" {
				body["reason"] = in.Reason
			}
			return c.Do(ctx, "POST", "/api/runs/"+id(in.AgentID)+"/plan_update", body)
		})
	for _, ctl := range []struct{ name, route, desc string }{
		{"pause_agent", "pause", "Ask the agent to pause."},
		{"resume_agent", "resume", "Ask a paused agent to continue."},
		{"stop_agent", "stop", "Ask the agent to wrap up and stop (soft stop)."},
	} {
		route := ctl.route
		tool(s, c, ctl.name, ctl.desc, func(ctx context.Context, in controlIn) (json.RawMessage, error) {
			body := map[string]any{}
			if in.Reason != "" {
				body["reason"] = in.Reason
			}
			return c.Do(ctx, "POST", "/api/runs/"+id(in.AgentID)+"/"+route, body)
		})
	}
	tool(s, c, "kill_agent", "Hard stop: kill the agent's process tree. Use only when stop_agent did not work.",
		func(ctx context.Context, in controlIn) (json.RawMessage, error) {
			return c.Do(ctx, "POST", "/api/runs/"+id(in.AgentID)+"/kill", map[string]any{})
		})
	tool(s, c, "review_diff", "The agent's committed changes against its base branch (diff text, stat, truncated flag) and anything still uncommitted in its worktree.",
		func(ctx context.Context, in diffIn) (json.RawMessage, error) {
			p := "/api/runs/" + id(in.AgentID) + "/diff"
			if in.MaxBytes > 0 {
				p += "?max_bytes=" + strconv.Itoa(in.MaxBytes)
			}
			return c.Do(ctx, "GET", p, nil)
		})
	tool(s, c, "merge_agent", "Request the merge of a finished agent's branch into its base. Only after review_diff and passing tests; needs confirm: true. Usually returns status pending: the user approves it, and you get an approval item from wait_for_events.",
		func(ctx context.Context, in mergeIn) (json.RawMessage, error) {
			body := map[string]any{"confirm": in.Confirm}
			if in.Note != "" {
				body["note"] = in.Note
			}
			return c.Do(ctx, "POST", "/api/runs/"+id(in.AgentID)+"/merge", body)
		})
	tool(s, c, "list_approvals", "Pending approval requests (merges, spawns) waiting for the user.",
		func(ctx context.Context, _ empty) (json.RawMessage, error) {
			return c.Do(ctx, "GET", "/api/approvals?status=pending", nil)
		})
	tool(s, c, "set_mode", "Switch how lineups are decided: user, propose or auto. Only when the user asked for it.",
		func(ctx context.Context, in modeIn) (json.RawMessage, error) {
			if _, err := c.Do(ctx, "PATCH", "/api/config", map[string]any{"mode": in.Mode}); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"ok": true, "mode": in.Mode})
		})
	tool(s, c, "set_account_enabled", "Enable or disable an account for new agents. Only when the user asked for it.",
		func(ctx context.Context, in accountEnabledIn) (json.RawMessage, error) {
			return c.Do(ctx, "PATCH", "/api/accounts/"+id(in.Account), map[string]any{"enabled": in.Enabled})
		})
	return s
}

// Serve runs the server over r/w (stdin/stdout for `baton mcp`) until the client disconnects
// or ctx ends.
func Serve(ctx context.Context, c *Client, version string, r io.Reader, w io.Writer) error {
	return NewServer(c, version).Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(r), Writer: nopWriteCloser{w}})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
