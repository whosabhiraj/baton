package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/whosabhiraj/baton/internal/config"
)

type claude struct{}

func (claude) Argv(acct config.Account, model, effort string) ([]string, error) {
	if acct.Permissions.Mode == config.ModeSandboxed {
		return nil, errors.New("sandboxed mode needs SandboxArgv")
	}
	argv := claudeBase(model, effort)
	argv = append(argv, permissionFlags(acct.Permissions)...)
	argv = append(argv, claudeToolFlags(acct.Permissions)...)
	return append(argv, acct.Permissions.ExtraArgs...), nil
}

func claudeBase(model, effort string) []string {
	argv := []string{"claude", "-p", "--output-format", "stream-json", "--verbose"}
	if model != "" && model != "default" {
		argv = append(argv, "--model", model)
	}
	if effort != "" {
		argv = append(argv, "--effort", effort)
	}
	return argv
}

func claudeToolFlags(p config.Permissions) []string {
	var out []string
	if len(p.AllowedTools) > 0 {
		out = append(out, "--allowedTools", strings.Join(p.AllowedTools, ","))
	}
	if len(p.DeniedTools) > 0 {
		out = append(out, "--disallowedTools", strings.Join(p.DeniedTools, ","))
	}
	return out
}

func (claude) Parser() Parser { return newClaudeParser() }

type usage struct{ in, out, cacheR, cacheW int64 }

// claudeParser reads `claude -p --output-format stream-json --verbose` lines. Assistant
// messages can repeat per content block with the same message id, so usage is kept per
// message id (latest wins) and summed.
type claudeParser struct {
	byMsg map[string]usage
	n     int
	model string
	last  string // last usage total sent: a repeated message with the same usage sends nothing
}

func newClaudeParser() *claudeParser { return &claudeParser{byMsg: map[string]usage{}} }

func (p *claudeParser) Parse(line []byte) []Event {
	var m struct {
		Type         string          `json:"type"`
		Message      json.RawMessage `json:"message"`
		Usage        json.RawMessage `json:"usage"`
		Model        string          `json:"model"`
		TotalCostUSD *float64        `json:"total_cost_usd"`
	}
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	switch m.Type {
	case "system":
		if m.Model != "" {
			p.model = m.Model
		}
	case "assistant":
		return p.assistant(m.Message)
	case "result":
		pl := usagePayload(parseUsage(m.Usage), p.model)
		if m.TotalCostUSD != nil && *m.TotalCostUSD >= 0 {
			pl["cost_usd"] = *m.TotalCostUSD
		}
		return []Event{{Type: "usage", Payload: pl}, {Type: Finished}} // claude -p ends after its result
	}
	return nil
}

func (p *claudeParser) assistant(raw json.RawMessage) []Event {
	var msg struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type  string         `json:"type"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return nil
	}
	if msg.Model != "" {
		p.model = msg.Model
	}
	var evs []Event
	for _, c := range msg.Content {
		if c.Type == "tool_use" {
			evs = append(evs, Event{Type: "activity", Payload: toolActivity(c.Name, c.Input)})
		}
	}
	if len(msg.Usage) > 0 {
		id := msg.ID
		if id == "" {
			p.n++
			id = fmt.Sprintf("anon-%d", p.n)
		}
		p.byMsg[id] = parseUsage(msg.Usage)
		var sum usage
		for _, u := range p.byMsg {
			sum.in += u.in
			sum.out += u.out
			sum.cacheR += u.cacheR
			sum.cacheW += u.cacheW
		}
		if key := fmt.Sprint(sum, p.model); key != p.last {
			p.last = key
			evs = append(evs, Event{Type: "usage", Payload: usagePayload(sum, p.model)})
		}
	}
	return evs
}

func parseUsage(raw json.RawMessage) usage {
	var u struct {
		In     int64 `json:"input_tokens"`
		Out    int64 `json:"output_tokens"`
		CacheR int64 `json:"cache_read_input_tokens"`
		CacheW int64 `json:"cache_creation_input_tokens"`
	}
	_ = json.Unmarshal(raw, &u)
	return usage{u.In, u.Out, u.CacheR, u.CacheW}
}

func usagePayload(u usage, model string) map[string]any {
	pl := map[string]any{"input_tokens": u.in, "output_tokens": u.out,
		"cache_read_tokens": u.cacheR, "cache_write_tokens": u.cacheW}
	if model != "" {
		pl["model"] = model
	}
	return pl
}

// toolActivity maps a Claude tool call to the activity schema.
func toolActivity(name string, in map[string]any) map[string]any {
	str := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := in[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	tool, detail := "other", ""
	switch name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		tool, detail = "edit", str("file_path", "notebook_path", "path")
	case "Bash", "PowerShell":
		tool, detail = "shell", str("command")
	case "Read":
		tool, detail = "read", str("file_path", "path")
	case "Grep", "Glob":
		tool, detail = "search", str("pattern", "path")
	case "WebFetch", "WebSearch":
		tool, detail = "web", str("url", "query")
	default:
		detail = str("description", "file_path", "command", "prompt")
	}
	if detail == "" {
		detail = name
	}
	return map[string]any{"tool": tool, "detail": trunc(detail, 200), "raw_kind": name}
}
