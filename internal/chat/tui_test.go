package chat

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/whosabhiraj/baton/internal/conductor"
	"github.com/whosabhiraj/baton/internal/proto"
)

func TestTUIModel(t *testing.T) {
	past := []conductor.Entry{{N: 1, Dir: "in", From: "human", Text: "split the parser work"}}
	m := newModel(&Session{C: &Client{URL: "http://127.0.0.1:1", Token: "x"}}, past)
	if m.View() != "connecting..." {
		t.Fatal("view before size")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	msg, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "Proposed lineup: two agents."},
		map[string]any{"type": "tool_use", "name": "mcp__baton__list_accounts", "input": map[string]any{}}}}})
	m.Update(entryMsg(conductor.Entry{N: 2, Dir: "out", Msg: msg}))
	m.Update(stateMsg(proto.ConductorState{State: "acting", Account: "claude-a", Model: "opus"}))
	m.Update(statusMsg(Status{Mode: "propose", Live: 2, Approvals: 1}))
	v := m.View()
	for _, want := range []string{"split the parser work", "Proposed lineup: two agents.", "list_accounts",
		"conductor: acting · claude-a opus", "mode propose", "2 agent(s)", "1 approval(s) pending"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in:\n%s", want, v)
		}
	}
	// local command output appears; ctrl+c quits
	m.Update(localMsg{{Kind: KindNotice, Text: "no live agents"}})
	if !strings.Contains(m.View(), "no live agents") {
		t.Fatal("notice not shown")
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil || !m.quit {
		t.Fatal("ctrl+c does not quit")
	}
}
