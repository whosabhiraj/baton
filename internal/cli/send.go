package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
)

type usageError struct{ msg string }

func (e *usageError) Error() string   { return e.msg }
func usagef(f string, a ...any) error { return &usageError{fmt.Sprintf(f, a...)} }

type payload = map[string]any

type runEnv struct {
	runID, agentID, dir, url, token string
}

func loadRunEnv(getenv func(string) string) (*runEnv, error) {
	e := &runEnv{
		runID: getenv("BATON_RUN_ID"), agentID: getenv("BATON_AGENT_ID"), dir: getenv("BATON_RUN_DIR"),
		url: strings.TrimRight(getenv("BATON_URL"), "/"), token: getenv("BATON_TOKEN"),
	}
	if e.runID == "" || e.agentID == "" || e.dir == "" {
		return nil, usagef("not inside a Baton run (BATON_RUN_ID, BATON_AGENT_ID, BATON_RUN_DIR must be set)")
	}
	return e, nil
}

// httpClient: no proxy, no keep-alive, 3 s overall.
func httpClient() *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, DisableKeepAlives: true,
			DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		},
	}
}

type wireMsg struct {
	V       int     `json:"v"`
	RunID   string  `json:"run_id"`
	AgentID string  `json:"agent_id"`
	TS      string  `json:"ts"`
	Type    string  `json:"type"`
	Payload payload `json:"payload"`
	MsgID   string  `json:"msg_id"`
}

// jsv renders a JSON-decoded value the way JS template strings do.
func jsv(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok {
		return "undefined"
	}
	return jsStr(v)
}

func jsStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			if x != nil {
				parts[i] = jsStr(x)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

func jsTruthy(m map[string]any, k string) bool {
	switch t := m[k].(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f != 0
	}
	return true
}

// send sends one agent message and returns the exit code.
func (c *ctx) send(typ string, p payload, quiet bool) (int, error) {
	vp, perr := proto.ValidateAgentPayload(typ, p)
	if perr != nil {
		return 0, usagef("invalid %s: %s", typ, perr.Msg)
	}
	e := c.env
	msg := wireMsg{V: 1, RunID: e.runID, AgentID: e.agentID, TS: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Type: typ, Payload: vp, MsgID: ids.UUID()}
	line := proto.MarshalLine(msg)
	if line == nil {
		return 0, usagef("invalid %s: payload not encodable", typ)
	}
	out := func(s string) {
		if !quiet {
			fmt.Fprintln(c.stdout, s)
		}
	}
	if e.url != "" && e.token != "" {
		if code, done := c.post(line, out); done {
			return code, nil
		}
	}
	if err := appendLine(filepath.Join(e.dir, paths.OutboxFile), e.dir, line); err != nil {
		fmt.Fprintf(c.stderr, "baton: warning: could not report (%s)\n", err.Error())
		return 0, nil
	}
	out("spooled (hub unreachable)")
	return 0, nil
}

func appendLine(file, dir string, line []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(line) // single write
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// post tries the hub. done=false means fall back to the spool.
func (c *ctx) post(line []byte, out func(string)) (code int, done bool) {
	req, err := http.NewRequest(http.MethodPost, c.env.url+"/api/ingest", bytes.NewReader(bytes.TrimRight(line, "\n")))
	if err != nil {
		return 0, false
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+c.env.token)
	res, err := c.client.Do(req)
	if err != nil {
		return 0, false
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var body map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&body) != nil || body == nil {
		body = map[string]any{}
	}
	has := func(k string) bool { v, ok := body[k]; return ok && v != nil }
	switch {
	case res.StatusCode >= 500:
		return 0, false
	case res.StatusCode >= 200 && res.StatusCode < 300:
		seq := "?"
		if has("seq") {
			seq = jsv(body, "seq")
		}
		dup := ""
		if jsTruthy(body, "duplicate") {
			dup = " (dup)"
		}
		out(fmt.Sprintf("ok #%s%s", seq, dup))
		return 0, true
	case res.StatusCode == 400:
		cd := "400"
		if has("code") {
			cd = jsv(body, "code")
		}
		er := ""
		if has("error") {
			er = jsv(body, "error")
		}
		fmt.Fprintln(c.stderr, strings.TrimSpace(fmt.Sprintf("baton: rejected: %s %s", cd, er)))
		return 2, true
	default:
		cd := fmt.Sprint(res.StatusCode)
		if has("code") {
			cd = jsv(body, "code")
		}
		fmt.Fprintf(c.stderr, "baton: rejected: %s\n", cd)
		return 0, true
	}
}
