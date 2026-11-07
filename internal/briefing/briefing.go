// Package briefing renders the per-agent brief (BATON_BRIEF.md) from an embedded template.
package briefing

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed brief.md
var templateText string

var tmpl = template.Must(template.New("brief").Option("missingkey=error").Parse(templateText))

// Step is one plan step.
type Step struct {
	ID, Title, Acceptance string
}

// Data is everything the brief needs. Empty Model renders as "default".
type Data struct {
	Callsign, AgentID, Role, Kind, Model, AccountLabel string
	Branch, Worktree, Task                             string
	Steps                                              []Step
	Paths, Forbidden, AskBefore                        []string
	// KindNotes overrides the built-in notes for Kind (used for custom kinds); "" = built-in.
	KindNotes string
	// ExtraNote is appended to the kind notes (e.g. "AGENTS.md already existed").
	ExtraNote string
	// Sandboxed adds the sandbox rules (permissions.mode: sandboxed).
	Sandboxed bool
}

// SandboxNote tells a sandboxed agent what it can and cannot do.
const SandboxNote = "You run in a sandbox: you can write only inside your worktree, there is no network, and some folders outside it cannot be read. Work with what is in the repo; if you need something outside (a package, a file, a site), say so with `baton ask` instead of trying to get around it. `baton` prints \"spooled (hub unreachable)\": that is expected here, your reports still arrive."

type view struct {
	Data
	Steps                       []viewStep
	Paths, Forbidden, AskBefore string
}

type viewStep struct {
	N                     int
	ID, Title, Acceptance string
}

// KindNotes returns the built-in notes for a kind ("" for unknown kinds).
func KindNotes(kind string) string {
	switch kind {
	case "claude":
		return "Mirror your internal todo list to Baton steps; use `baton` for anything the conductor needs to see. Don't spawn subagents unless the plan says to."
	case "codex":
		return "Codex reads `AGENTS.md`; Baton also writes the rules above there. Keep `baton` calls as plain shell commands. Prefer non-interactive flags."
	case "opencode":
		return "Use only the configured API-key provider. Report via the `baton` shell commands."
	case "generic":
		return "You only need to run the `baton` commands above. If you can't run commands, append the same JSON messages to `$BATON_RUN_DIR/events.jsonl`, one per line."
	}
	return ""
}

func list(l []string, empty string) string {
	var out []string
	for _, s := range l {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return empty
	}
	return strings.Join(out, ", ")
}

// Render fills the template.
func Render(d Data) (string, error) {
	if d.Model == "" {
		d.Model = "default"
	}
	notes := d.KindNotes
	if notes == "" {
		notes = KindNotes(d.Kind)
	}
	if notes == "" {
		notes = "No kind-specific notes. You only need to run the `baton` commands above."
	}
	if d.ExtraNote != "" {
		notes += "\n" + d.ExtraNote
	}
	if d.Sandboxed {
		notes += "\n" + SandboxNote
	}
	d.KindNotes = strings.TrimSpace(notes)
	v := view{Data: d,
		Paths:     list(d.Paths, "anywhere in your worktree"),
		Forbidden: list(d.Forbidden, "(nothing listed)"),
		AskBefore: list(d.AskBefore, "(nothing listed)"),
	}
	for i, s := range d.Steps {
		acc := s.Acceptance
		if strings.TrimSpace(acc) == "" {
			acc = "(not specified; verify with a command and show it as evidence)"
		}
		v.Steps = append(v.Steps, viewStep{N: i + 1, ID: s.ID, Title: s.Title, Acceptance: acc})
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, v); err != nil {
		return "", fmt.Errorf("briefing: %w", err)
	}
	return b.String(), nil
}
