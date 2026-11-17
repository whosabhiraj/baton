// Package permit is `baton __permit`: an MCP stdio server that Claude Code uses as its
// --permission-prompt-tool. Every permission prompt Claude would show a human becomes a Baton
// access request (PROTOCOL.md A20); the user's answer goes back as allow or deny.
package permit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	EnvURL       = "BATON_PERMIT_URL"
	EnvToken     = "BATON_PERMIT_TOKEN"
	EnvRun       = "BATON_PERMIT_RUN"
	EnvTimeout   = "BATON_PERMIT_TIMEOUT_S"
	ServerName   = "baton_access"
	ToolName     = "approve"
	FullToolName = "mcp__" + ServerName + "__" + ToolName

	defaultTimeout = 900 * time.Second
	maxSummary     = 300
	maxInput       = 2000
)

type Options struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Getenv  func(string) string // nil = os.Getenv
	Version string
	HTTP    *http.Client // nil = no proxy, no overall timeout (long-polls)
}

// Client talks to the hub's access API for one run.
type Client struct {
	URL, Token, Run string
	Timeout         time.Duration // how long to wait for the user, from the call start
	HTTP            *http.Client
	Stderr          io.Writer // optional; one line per hub failure
}

// Run serves the MCP server on stdio until stdin closes. Missing config does not stop it:
// every call then answers deny.
func Run(o Options) int {
	get := o.Getenv
	if get == nil {
		get = os.Getenv
	}
	timeout := defaultTimeout
	if f, err := strconv.ParseFloat(strings.TrimSpace(get(EnvTimeout)), 64); err == nil && f > 0 {
		timeout = time.Duration(f * float64(time.Second))
	}
	c := &Client{URL: strings.TrimRight(get(EnvURL), "/"), Token: get(EnvToken), Run: get(EnvRun),
		Timeout: timeout, HTTP: o.HTTP, Stderr: o.Stderr}
	tr := &mcp.IOTransport{Reader: io.NopCloser(o.Stdin), Writer: nopWriteCloser{o.Stdout}}
	if err := NewServer(c, o.Version).Run(context.Background(), tr); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		if o.Stderr != nil {
			fmt.Fprintln(o.Stderr, "baton __permit:", err)
		}
		return 1
	}
	return 0
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// NewServer registers the one tool.
func NewServer(c *Client, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: version},
		&mcp.ServerOptions{Instructions: "Baton: asks the user to approve a tool call."})
	// Hand-written and permissive: Claude may add fields, and strict validation would break the call.
	s.AddTool(&mcp.Tool{
		Name:        ToolName,
		Description: "Ask the user (through Baton) whether a tool call may run.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool_name":   map[string]any{"type": "string"},
				"input":       map[string]any{"type": "object"},
				"tool_use_id": map[string]any{"type": "string"},
			},
			"additionalProperties": true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(c.approve(ctx, req.Params.Arguments)), nil
	})
	return s
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func deny(msg string) string {
	b, _ := json.Marshal(map[string]string{"behavior": "deny", "message": msg})
	return string(b)
}

func (c *Client) approve(ctx context.Context, args json.RawMessage) string {
	var a struct {
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(args, &a) // junk arguments just mean an unknown tool
	var input map[string]any
	if err := json.Unmarshal(a.Input, &input); err != nil || input == nil {
		input = map[string]any{}
	}
	// The allow answer must echo the input exactly, so keep the raw bytes.
	echo := "{}"
	var buf bytes.Buffer
	if len(input) > 0 && json.Compact(&buf, a.Input) == nil {
		echo = buf.String()
	}

	kind, summary, detail := describe(a.ToolName, input)
	cctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	id, err := c.create(cctx, kind, summary, detail)
	if err != nil {
		return c.fail(err)
	}
	status, note := c.wait(cctx, id)
	switch status {
	case "approved":
		return `{"behavior":"allow","updatedInput":` + echo + `}`
	case "pending":
		// Timed out or Claude cancelled: withdraw the request so it does not linger on the dashboard.
		bg, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = c.do(bg, "POST", c.path(id)+"/cancel", map[string]any{}, nil)
		if ctx.Err() != nil {
			return deny("The call was cancelled, so it is not allowed.")
		}
		return deny("Nobody answered in time, so it is not allowed.")
	}
	if note != "" {
		return deny("The user rejected this in Baton: " + note)
	}
	return deny("The user rejected this in Baton.")
}

func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return defaultTimeout
	}
	return c.Timeout
}

func (c *Client) fail(err error) string {
	if c.Stderr != nil {
		fmt.Fprintln(c.Stderr, "baton __permit:", err)
	}
	return deny("Baton could not ask the user (" + err.Error() + "), so it is not allowed.")
}

// describe maps a Claude tool call to an access request. Detail values are strings only
// because the dashboard shows them as rows.
func describe(tool string, in map[string]any) (kind, summary string, detail map[string]any) {
	str := func(k string) string { s, _ := in[k].(string); return s }
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "unknown tool"
	}
	detail = map[string]any{"tool": tool}
	switch tool {
	case "Bash", "PowerShell":
		kind = "command"
		cmd := str("command")
		detail["command"] = cmd
		summary = "run: " + cmd
		if d := str("description"); strings.TrimSpace(d) != "" {
			summary = d
			detail["description"] = d
		}
		if in["dangerouslyDisableSandbox"] == true {
			detail["unsandboxed"] = "yes: this command would run outside the sandbox, with your full access"
			summary = "outside the sandbox: " + summary
		}
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		kind = "file_change"
		path := str("file_path")
		if path == "" {
			path = str("notebook_path")
		}
		detail["path"] = path
		verb := "edit "
		if tool == "Write" {
			verb = "write "
		}
		summary = verb + path
	case "WebFetch":
		kind = "tool"
		detail["url"] = str("url")
		summary = "fetch " + str("url")
	case "WebSearch":
		kind = "tool"
		detail["query"] = str("query")
		summary = "search the web: " + str("query")
	default:
		kind = "tool"
		b, _ := json.Marshal(in)
		detail["input"] = capRunes(string(b), maxInput)
		summary = "use " + tool
	}
	return kind, capRunes(oneLine(summary), maxSummary), detail
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ") // collapses \r, \n, tabs and runs of spaces
}

func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ---- hub (same logic as codexapp's hubClient) ----

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Transport: &http.Transport{Proxy: nil}}
}

func (c *Client) path(id string) string {
	return "/api/runs/" + url.PathEscape(c.Run) + "/access/" + url.PathEscape(id)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) (int, error) {
	if c.URL == "" || c.Token == "" || c.Run == "" {
		return 0, errors.New("no hub URL, token or run id configured")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		s := strings.TrimSpace(string(b))
		if r := []rune(s); len(r) > 200 {
			s = string(r[:200]) + "..."
		}
		return resp.StatusCode, fmt.Errorf("hub answered %d: %s", resp.StatusCode, s)
	}
	if out != nil {
		return 200, json.Unmarshal(b, out)
	}
	return 200, nil
}

func (c *Client) create(ctx context.Context, kind, summary string, detail map[string]any) (string, error) {
	var r struct {
		Approval struct {
			ID string `json:"id"`
		} `json:"approval"`
	}
	body := map[string]any{"kind": kind, "summary": summary, "detail": detail}
	if _, err := c.do(ctx, "POST", "/api/runs/"+url.PathEscape(c.Run)+"/access", body, &r); err != nil {
		return "", err
	}
	if r.Approval.ID == "" {
		return "", errors.New("hub returned no approval id")
	}
	return r.Approval.ID, nil
}

// wait long-polls until decided or ctx ends ("pending" then). Network errors are retried.
func (c *Client) wait(ctx context.Context, id string) (status, note string) {
	for ctx.Err() == nil {
		var r struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		code, err := c.do(ctx, "GET", c.path(id)+"?wait=55", nil, &r)
		switch {
		case err == nil && r.Status != "" && r.Status != "pending":
			return r.Status, r.Note
		case code >= 400 && code < 500:
			return "rejected", "" // gone or not ours: treat as a no
		case err != nil:
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
	return "pending", ""
}
