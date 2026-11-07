package adapters

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/whosabhiraj/baton/internal/config"
)

type codex struct{}

func (codex) Argv(acct config.Account, model, effort string) ([]string, error) {
	p := acct.Permissions
	if len(p.AllowedTools) > 0 || len(p.DeniedTools) > 0 {
		return nil, errors.New("allowed_tools/denied_tools are claude-only")
	}
	if p.Mode == config.ModeSandboxed {
		return nil, errors.New("sandboxed mode needs SandboxArgv")
	}
	argv := codexBase(model, effort)
	switch p.Mode {
	case "", "permissive":
		argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
	case "acceptEdits":
		argv = append(argv, "--sandbox", "workspace-write", "--approve-for-me")
	}
	argv = append(argv, p.ExtraArgs...)
	return append(argv, "-"), nil // brief on stdin
}

func codexBase(model, effort string) []string {
	argv := []string{"codex", "exec", "--json"}
	if model != "" && model != "default" {
		argv = append(argv, "-m", model)
	}
	if e := codexEffort(effort); e != "" {
		argv = append(argv, "-c", `model_reasoning_effort="`+e+`"`)
	}
	return argv
}

// codexEffort maps Baton's effort to codex's model_reasoning_effort ("" = leave it).
func codexEffort(effort string) string {
	switch effort {
	case "":
		return ""
	case "xhigh", "max":
		return "high"
	}
	return effort
}

func (codex) Parser() Parser { return &codexParser{} }

// codexParser reads `codex exec --json` JSONL (thread/turn/item events). Usage is summed
// across turns. Unknown lines are ignored.
type codexParser struct {
	sum   usage
	model string
}

func (p *codexParser) Parse(line []byte) []Event {
	var probe struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(line, &probe) == nil && probe.Method != "" {
		return p.parseApp(probe.Method, probe.Params) // `baton __codex`: app-server notifications
	}
	var m struct {
		Type  string `json:"type"`
		Model string `json:"model"`
		Item  struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Query   string `json:"query"`
			Server  string `json:"server"`
			Tool    string `json:"tool"`
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"item"`
		Usage struct {
			In     int64 `json:"input_tokens"` // includes the cached tokens (OpenAI style)
			CacheR int64 `json:"cached_input_tokens"`
			CacheW int64 `json:"cache_write_input_tokens"`
			Out    int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	if m.Model != "" {
		p.model = m.Model
	}
	act := func(tool, detail, raw string) []Event {
		if detail == "" {
			detail = raw
		}
		return []Event{{Type: "activity", Payload: map[string]any{"tool": tool, "detail": trunc(detail, 200), "raw_kind": raw}}}
	}
	switch m.Type {
	case "item.started":
		switch m.Item.Type {
		case "command_execution":
			return act("shell", unwrapShell(m.Item.Command), "command_execution")
		case "web_search":
			return act("web", m.Item.Query, "web_search")
		case "mcp_tool_call":
			return act("other", strings.Trim(m.Item.Server+"."+m.Item.Tool, "."), "mcp_tool_call")
		}
	case "item.completed":
		if m.Item.Type == "file_change" {
			var paths []string
			for _, c := range m.Item.Changes {
				if c.Path != "" {
					paths = append(paths, c.Path)
				}
			}
			return act("edit", strings.Join(paths, ", "), "file_change")
		}
	case "turn.completed":
		// Report input without the cache parts, like claude does, so totals never double count.
		p.sum.in += max(0, m.Usage.In-m.Usage.CacheR) // cached part verified on a real run; cache writes not yet seen
		p.sum.out += m.Usage.Out
		p.sum.cacheR += m.Usage.CacheR
		p.sum.cacheW += m.Usage.CacheW
		// codex exec runs one turn: this is the end (codex can hang here under the Windows sandbox)
		return []Event{{Type: "usage", Payload: usagePayload(p.sum, p.model)}, {Type: Finished}}
	}
	return nil
}

// shellWrap matches the wrapper codex puts around commands ("...powershell.exe" -Command '...',
// /bin/bash -lc '...', cmd /c ...), so the activity line shows the command itself.
var shellWrap = regexp.MustCompile(`(?is)^(?:"[^"]*?\b` + shells + `"|\S*?\b` + shells + `)\s+(?:-NoProfile\s+)?(?:-Command|-lc|-c|/c)\s+(.+)$`)

const shells = `(?:powershell|pwsh|bash|zsh|sh|cmd)(?:\.exe)?`

func unwrapShell(cmd string) string {
	m := shellWrap.FindStringSubmatch(strings.TrimSpace(cmd))
	if m == nil {
		return cmd
	}
	inner := m[1]
	if n := len(inner); n >= 2 && (inner[0] == '\'' || inner[0] == '"') && inner[n-1] == inner[0] {
		inner = inner[1 : n-1]
	}
	return inner
}

// parseApp reads the app-server notifications `baton __codex` writes (plus its own
// baton/access lines). Token usage arrives as running totals for the thread.
func (p *codexParser) parseApp(method string, params json.RawMessage) []Event {
	act := func(tool, detail, raw string) []Event {
		if detail == "" {
			detail = raw
		}
		return []Event{{Type: "activity", Payload: map[string]any{"tool": tool, "detail": trunc(detail, 200), "raw_kind": raw}}}
	}
	var m struct {
		Item struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Query   string `json:"query"`
			Server  string `json:"server"`
			Tool    string `json:"tool"`
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"item"`
		TokenUsage struct {
			Total struct {
				In     int64 `json:"inputTokens"`
				CacheR int64 `json:"cachedInputTokens"`
				CacheW int64 `json:"cacheWriteInputTokens"`
				Out    int64 `json:"outputTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(params, &m) != nil {
		return nil
	}
	switch method {
	case "item/started":
		switch m.Item.Type {
		case "commandExecution":
			return act("shell", unwrapShell(m.Item.Command), "command_execution")
		case "webSearch":
			return act("web", m.Item.Query, "web_search")
		case "mcpToolCall":
			return act("other", strings.Trim(m.Item.Server+"."+m.Item.Tool, "."), "mcp_tool_call")
		}
	case "item/completed":
		if m.Item.Type == "fileChange" {
			var paths []string
			for _, c := range m.Item.Changes {
				if c.Path != "" {
					paths = append(paths, c.Path)
				}
			}
			return act("edit", strings.Join(paths, ", "), "file_change")
		}
	case "thread/tokenUsage/updated":
		t := m.TokenUsage.Total
		p.sum = usage{in: max(0, t.In-t.CacheR), out: t.Out, cacheR: t.CacheR, cacheW: t.CacheW}
		return []Event{{Type: "usage", Payload: usagePayload(p.sum, p.model)}}
	case "turn/completed":
		return []Event{{Type: Finished}}
	case "baton/access":
		return act("other", "access request ("+m.Kind+", "+m.Status+"): "+m.Summary, "access_request")
	}
	return nil
}
