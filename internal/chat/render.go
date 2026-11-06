package chat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/whosabhiraj/baton/internal/conductor"
)

// Line kinds.
const (
	KindYou       = "you"       // what the user typed
	KindHub       = "hub"       // a [baton] nudge from the hub
	KindConductor = "conductor" // the conductor's text
	KindTool      = "tool"      // a tool call, one line
	KindError     = "error"     // a failed tool call or turn
	KindMeta      = "meta"      // turn summary, process events
	KindNotice    = "notice"    // local messages from the chat itself (command output)
)

// Line is one rendered piece of the conversation (Text may span several lines).
type Line struct {
	Kind string
	Text string
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " ..."
	}
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

// toolSummary turns a tool call into "spawn_agent account=claude-b role=implementer".
func toolSummary(name string, input map[string]any) string {
	name = strings.TrimPrefix(name, "mcp__baton__")
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	prio := map[string]int{"agent_id": 0, "account": 1, "role": 2, "model": 3, "effort": 4, "qid": 5, "mode": 6, "file_path": 7, "pattern": 8}
	sort.SliceStable(keys, func(i, j int) bool {
		pi, iok := prio[keys[i]]
		pj, jok := prio[keys[j]]
		if iok != jok {
			return iok
		}
		return iok && pi < pj
	})
	var parts []string
	for _, k := range keys {
		if len(parts) == 3 {
			parts = append(parts, "...")
			break
		}
		switch v := input[k].(type) {
		case string:
			parts = append(parts, k+"="+clip(v, 40))
		case bool, float64:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	if len(parts) == 0 {
		return name
	}
	return name + " " + strings.Join(parts, " ")
}

type block struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   map[string]any  `json:"input"`
	IsError bool            `json:"is_error"`
	Content json.RawMessage `json:"content"`
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b []string
		for _, p := range parts {
			b = append(b, p.Text)
		}
		return strings.Join(b, " ")
	}
	return string(raw)
}

// Render turns one history entry into display lines (none for noise).
func Render(e conductor.Entry) []Line {
	switch e.Dir {
	case "in":
		if e.From == "hub" {
			return []Line{{KindHub, e.Text}}
		}
		return []Line{{KindYou, e.Text}}
	case "hub":
		switch e.Event {
		case "error", "exited":
			return []Line{{KindError, "conductor " + e.Event + ": " + e.Text}}
		}
		return []Line{{KindMeta, "conductor " + e.Event + ": " + e.Text}}
	}
	var m struct {
		Type     string   `json:"type"`
		Subtype  string   `json:"subtype"`
		IsError  bool     `json:"is_error"`
		Result   string   `json:"result"`
		Duration float64  `json:"duration_ms"`
		Cost     *float64 `json:"total_cost_usd"`
		Text     string   `json:"text"` // raw lines
		Message  struct {
			Content []block `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(e.Msg, &m) != nil {
		return nil
	}
	var out []Line
	switch m.Type {
	case "assistant":
		for _, b := range m.Message.Content {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					out = append(out, Line{KindConductor, t})
				}
			case "tool_use":
				out = append(out, Line{KindTool, toolSummary(b.Name, b.Input)})
			}
		}
	case "user":
		for _, b := range m.Message.Content {
			if b.Type == "tool_result" && b.IsError {
				out = append(out, Line{KindError, clip(resultText(b.Content), 300)})
			}
		}
	case "result":
		if m.IsError || (m.Subtype != "" && m.Subtype != "success") {
			msg := m.Subtype
			if m.Result != "" {
				msg += ": " + clip(m.Result, 300)
			}
			return []Line{{KindError, "turn failed: " + msg}}
		}
		s := fmt.Sprintf("%.1fs", m.Duration/1000)
		if m.Cost != nil && *m.Cost > 0 {
			s += fmt.Sprintf(" · $%.4f", *m.Cost)
		}
		out = append(out, Line{KindMeta, s})
	case "raw":
		out = append(out, Line{KindMeta, clip(m.Text, 300)})
	}
	return out
}
