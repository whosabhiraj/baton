package adapters

import (
	"encoding/json"
	"strings"

	"github.com/whosabhiraj/baton/internal/config"
)

// custom is a kind defined in config: command + args + extra_args, brief on stdin.
type custom struct{ k config.CustomKind }

func (c custom) Argv(acct config.Account, _, _ string) ([]string, error) {
	argv := append([]string{c.k.Command}, c.k.Args...)
	return append(argv, acct.Permissions.ExtraArgs...), nil
}

func (c custom) Env() map[string]string { return c.k.Env }

func (c custom) Parser() Parser {
	switch c.k.Parser {
	case "stream-json":
		return newClaudeParser()
	case "jsonl":
		return jsonlParser{}
	}
	return noneParser{}
}

type noneParser struct{}

func (noneParser) Parse([]byte) []Event { return nil }

// jsonlParser emits one "other" activity per JSON line, detailed by its "type" field.
type jsonlParser struct{}

func (jsonlParser) Parse(line []byte) []Event {
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	detail, _ := m["type"].(string)
	if detail == "" {
		detail = string(line)
	}
	return []Event{{Type: "activity", Payload: map[string]any{"tool": "other", "detail": trunc(detail, 200), "raw_kind": "jsonl"}}}
}

// trunc shortens s to at most n runes, flattening whitespace.
func trunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
