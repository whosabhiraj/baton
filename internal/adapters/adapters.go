// Package adapters turns an account's CLI kind into a command line and a parser for the
// agent's stdout. Adapters are passive: they never run anything themselves.
package adapters

import (
	"fmt"
	"strings"

	"github.com/whosabhiraj/baton/internal/config"
)

// Event is one hub event derived from agent output: Type is "activity" or "usage".
type Event struct {
	Type    string
	Payload map[string]any
}

// Parser turns one line of agent stdout into zero or more events. It may keep state (usage
// totals), so use one Parser per run. Unknown or malformed lines yield nothing.
type Parser interface {
	Parse(line []byte) []Event
}

// Adapter builds the command line (the brief always goes via stdin, never argv) and a fresh
// parser for one kind of CLI.
type Adapter interface {
	Argv(acct config.Account, model, effort string) ([]string, error)
	Parser() Parser
}

// EnvProvider is implemented by adapters that add environment variables (custom kinds).
type EnvProvider interface {
	Env() map[string]string
}

// For returns the adapter for a run kind. Built-in kinds come first; any other name (and
// "generic") must be defined under kinds in the config.
func For(kind string, cfg config.Config) (Adapter, error) {
	switch kind {
	case "claude":
		return claude{}, nil
	case "codex":
		return nil, fmt.Errorf("codex adapter is not implemented yet (Phase 5b)")
	case "opencode":
		return nil, fmt.Errorf("opencode adapter is not implemented yet (Phase 5c)")
	}
	ck, ok := cfg.Kinds[kind]
	if !ok {
		if kind == "generic" || kind == "" {
			return nil, fmt.Errorf("define kinds.generic in the config (command, args, parser) to run generic agents")
		}
		return nil, fmt.Errorf("unknown agent kind %q: define it under kinds in the config", kind)
	}
	if strings.TrimSpace(ck.Command) == "" {
		return nil, fmt.Errorf("kinds.%s has no command", kind)
	}
	return custom{k: ck}, nil
}

// permissionFlags maps the permission mode; empty means permissive (workers run unattended).
func permissionFlags(p config.Permissions) []string {
	switch p.Mode {
	case "", "permissive":
		return []string{"--permission-mode", "bypassPermissions"}
	case "acceptEdits":
		return []string{"--permission-mode", "acceptEdits"}
	}
	return nil // "default": the CLI's own prompts
}
