package adapters

import (
	"encoding/json"
	"errors"
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
			In     int64 `json:"input_tokens"`
			CacheR int64 `json:"cached_input_tokens"`
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
			return act("shell", m.Item.Command, "command_execution")
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
		p.sum.in += m.Usage.In
		p.sum.out += m.Usage.Out
		p.sum.cacheR += m.Usage.CacheR
		return []Event{{Type: "usage", Payload: usagePayload(p.sum, p.model)}}
	}
	return nil
}
