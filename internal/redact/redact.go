// Package redact removes secrets from strings and JSON-like values before they are stored,
// fanned out or logged. Go's RE2 has no lookahead, so the key=value
// rule checks for an already-redacted value in code.
package redact

import (
	"regexp"
	"strings"
)

const r = "[REDACTED]"

type rule struct {
	re  *regexp.Regexp
	rep string // used with ReplaceAllString (supports ${1})
}

// Applied in order; more specific patterns first.
var rules = []rule{
	{regexp.MustCompile(`-----BEGIN [A-Z ]{0,30}PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]{0,30}PRIVATE KEY-----|$)`), "-----PRIVATE KEY " + r + "-----"},
	{regexp.MustCompile(`\bbt_run_[A-Za-z0-9_-]{8,}`), "bt_run_" + r},
	{regexp.MustCompile(`\bbt_cond_[A-Za-z0-9_-]{8,}`), "bt_cond_" + r},
	{regexp.MustCompile(`\bbt_adm_[A-Za-z0-9_-]{8,}`), "bt_adm_" + r},
	{regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{8,}`), "sk-ant-" + r},
	{regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}`), "sk-" + r},
	{regexp.MustCompile(`\b(gh[pousr]_)[A-Za-z0-9]{20,}`), "${1}" + r},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), "github_pat_" + r},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), "AKIA" + r},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), "xox-" + r},
	{regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`), "AIza" + r},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`), "eyJ" + r},
	{regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}" + r},
}

// keyValue matches key = value / key: value / "key": "value" for sensitive-looking key names.
// Group 1 is everything up to the value, group 2 the value. The TypeScript original uses a
// negative lookahead for an already redacted value; see redactKeyValues.
var keyValue = regexp.MustCompile(`(?i)\b((?:[A-Za-z0-9]{1,30}[_.-])?(?:api[_-]?key|secret|token|password|passwd|pwd|key)(?:[_.-][A-Za-z0-9]{1,30})?["']?\s*[:=]\s*["']?)([^\s"',;}\]]{3,200})`)

// String redacts secrets inside s.
func String(s string) string {
	for _, ru := range rules {
		s = ru.re.ReplaceAllString(s, ru.rep)
	}
	return redactKeyValues(s)
}

// redactKeyValues replaces values of sensitive-looking keys, leaving values that already
// start with "[REDACTED]" untouched (the replacement for the lookahead in the TS rule).
func redactKeyValues(s string) string {
	idx := keyValue.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var sb strings.Builder
	last := 0
	for _, m := range idx {
		valStart, valEnd := m[4], m[5]
		// The value class stops at ']', so "[REDACTED]" shows up as "[REDACTED" + "]".
		if s[valStart:valEnd] == r[:len(r)-1] && valEnd < len(s) && s[valEnd] == ']' {
			continue
		}
		// A value that already contains a (partial) redaction marker, e.g. "ghp_[REDACTED" left by
		// an earlier rule, also swallows the closing ']' so the output is not "[REDACTED]]".
		if valEnd < len(s) && s[valEnd] == ']' && strings.Contains(s[valStart:valEnd], "[REDACTED") {
			valEnd++
		}
		sb.WriteString(s[last:valStart])
		sb.WriteString(r)
		last = valEnd
	}
	sb.WriteString(s[last:])
	return sb.String()
}

var sensitiveKey = regexp.MustCompile(`(?i)^(?:[A-Za-z0-9_-]*[_-])?(?:api[_-]?key|secret|token|password|passwd|pwd|authorization)$`)

const maxDepth = 50

// Value deep-walks a JSON-like value (map[string]any, []any, []string, map[string]string,
// string) and returns a redacted copy: secrets inside strings are replaced and string values
// of sensitive keys become "[REDACTED]". Nesting deeper than 50 levels becomes "[REDACTED]".
// Other types are returned unchanged.
func Value(v any) any { return walk(v, 0) }

func walk(v any, depth int) any {
	switch x := v.(type) {
	case string:
		return String(x)
	case map[string]any:
		if depth > maxDepth {
			return r
		}
		out := make(map[string]any, len(x))
		for k, val := range x {
			if _, ok := val.(string); ok && sensitiveKey.MatchString(k) {
				out[k] = r
			} else {
				out[k] = walk(val, depth+1)
			}
		}
		return out
	case []any:
		if depth > maxDepth {
			return r
		}
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = walk(val, depth+1)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, val := range x {
			out[i] = String(val)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(x))
		for k, val := range x {
			if sensitiveKey.MatchString(k) {
				out[k] = r
			} else {
				out[k] = String(val)
			}
		}
		return out
	default:
		return v
	}
}
