package briefing_test

import (
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/briefing"
)

func TestRenderDefaults(t *testing.T) {
	out, err := briefing.Render(briefing.Data{Callsign: "Vesper", AgentID: "gn-1-0001", Role: "impl", Kind: "weird",
		AccountLabel: "A", Branch: "baton/x", Worktree: "/w", Task: "T",
		Steps: []briefing.Step{{ID: "s1", Title: "One"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Runtime: weird/default", "anywhere in your worktree", "Do not touch: (nothing listed)",
		"1. [s1] One", "not specified", "No kind-specific notes"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "{{") {
		t.Error("unrendered placeholder")
	}
}

func TestNoPlanAndKindNotes(t *testing.T) {
	for _, k := range []string{"claude", "codex", "opencode", "generic"} {
		out, err := briefing.Render(briefing.Data{Kind: k, Task: "T"})
		if err != nil || !strings.Contains(out, briefing.KindNotes(k)) || !strings.Contains(out, "No plan was supplied") {
			t.Fatalf("%s: %v", k, err)
		}
	}
}

func TestAskNote(t *testing.T) {
	d := briefing.Data{Callsign: "V", AgentID: "a", Role: "impl", Kind: "claude", Task: "T"}
	out, _ := briefing.Render(d)
	if strings.Contains(out, briefing.AskNote) {
		t.Fatal("ask note without Asks")
	}
	d.Asks = true
	if out, _ = briefing.Render(d); !strings.Contains(out, briefing.AskNote) {
		t.Fatal("no ask note")
	}
}
