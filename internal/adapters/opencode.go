package adapters

import (
	"encoding/json"
	"errors"

	"github.com/whosabhiraj/baton/internal/config"
)

type opencode struct{}

// Argv: `opencode run --format json` reads the message from stdin when stdin is not a
// terminal, so the brief never goes through argv. Permissions: OpenCode has no mode flags;
// `--auto` auto-approves anything not explicitly denied (permissive), otherwise unanswered
// permission requests are auto-rejected (acceptEdits/default behave the same). Effort is
// ignored: `--variant` names are provider-specific and an unknown one may fail the run.
func (opencode) Argv(acct config.Account, model, _ string) ([]string, error) {
	p := acct.Permissions
	if len(p.AllowedTools) > 0 || len(p.DeniedTools) > 0 {
		return nil, errors.New("allowed_tools/denied_tools are claude-only")
	}
	argv := []string{"opencode", "run", "--format", "json"}
	if model != "" && model != "default" {
		argv = append(argv, "-m", model) // provider/model
	}
	if p.Mode == "" || p.Mode == "permissive" {
		argv = append(argv, "--auto")
	}
	return append(argv, p.ExtraArgs...), nil
}

func (opencode) Parser() Parser { return &opencodeParser{} }

// opencodeParser reads `opencode run --format json` lines: {type, timestamp, sessionID, part}
// with type tool_use | step_start | step_finish | text | reasoning | error. Usage is summed
// over step_finish parts. Unknown lines are ignored.
type opencodeParser struct {
	sum  usage
	cost float64
}

func (p *opencodeParser) Parse(line []byte) []Event {
	var m struct {
		Type string `json:"type"`
		Part struct {
			Tool  string  `json:"tool"`
			Cost  float64 `json:"cost"`
			State struct {
				Input map[string]any `json:"input"`
			} `json:"state"`
			Tokens struct {
				In        int64 `json:"input"`
				Out       int64 `json:"output"`
				Reasoning int64 `json:"reasoning"`
				Cache     struct {
					Read  int64 `json:"read"`
					Write int64 `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		} `json:"part"`
	}
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	switch m.Type {
	case "tool_use":
		if m.Part.Tool == "" {
			return nil
		}
		return []Event{{Type: "activity", Payload: opencodeActivity(m.Part.Tool, m.Part.State.Input)}}
	case "step_finish":
		t := m.Part.Tokens
		p.sum.in += t.In
		p.sum.out += t.Out + t.Reasoning
		p.sum.cacheR += t.Cache.Read
		p.sum.cacheW += t.Cache.Write
		if m.Part.Cost > 0 {
			p.cost += m.Part.Cost
		}
		pl := usagePayload(p.sum, "")
		if p.cost > 0 {
			pl["cost_usd"] = p.cost
		}
		return []Event{{Type: "usage", Payload: pl}}
	}
	return nil
}

func opencodeActivity(name string, in map[string]any) map[string]any {
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
	case "edit", "write", "patch", "multiedit":
		tool, detail = "edit", str("filePath", "path")
	case "bash":
		tool, detail = "shell", str("command")
	case "read", "list":
		tool, detail = "read", str("filePath", "path")
	case "grep", "glob":
		tool, detail = "search", str("pattern", "path")
	case "webfetch", "websearch", "codesearch":
		tool, detail = "web", str("url", "query")
	default:
		detail = str("description", "filePath", "command", "prompt")
	}
	if detail == "" {
		detail = name
	}
	return map[string]any{"tool": tool, "detail": trunc(detail, 200), "raw_kind": name}
}
