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
	argv := []string{"codex", "exec", "--json"}
	if model != "" && model != "default" {
		argv = append(argv, "-m", model)
	}
	switch effort {
	case "":
	case "xhigh", "max":
		argv = append(argv, "-c", `model_reasoning_effort="high"`)
	default:
		argv = append(argv, "-c", `model_reasoning_effort="`+effort+`"`)
	}
	switch p.Mode {
	case "", "permissive":
		argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
	case "acceptEdits":
		argv = append(argv, "--sandbox", "workspace-write", "--approve-for-me")
	}
	argv = append(argv, p.ExtraArgs...)
	return append(argv, "-"), nil // brief on stdin
}

func (codex) Parser() Parser { return &codexParser{} }

// codexParser reads `codex exec --json` JSONL (thread/turn/item events). Usage is summed
// across turns. Unknown lines are ignored.
type codexParser struct {
	sum   usage
	model string
}

func (p *codexParser) Parse(line []byte) []Event {
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
		return []Event{{Type: "usage", Payload: usagePayload(p.sum, p.model)}}
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
