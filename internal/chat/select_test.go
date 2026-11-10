package chat

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/whosabhiraj/baton/internal/conductor"
)

func TestSelectedText(t *testing.T) {
	lines := []string{stCondTag.Render("conductor") + "   ", "hello world  ", "second line"}
	cases := []struct {
		s    selection
		want string
	}{
		{selection{a: cell{1, 0}, b: cell{1, 4}}, "hello"},
		{selection{a: cell{1, 6}, b: cell{2, 5}}, "world\nsecond"},
		{selection{a: cell{2, 5}, b: cell{1, 6}}, "world\nsecond"}, // dragged backwards
		{selection{a: cell{0, 0}, b: cell{1, 40}}, "conductor\nhello world"},
		{selection{a: cell{1, 3}, b: cell{1, 3}}, ""}, // a click selects nothing
	}
	for i, c := range cases {
		if got := selectedText(lines, c.s); got != c.want {
			t.Errorf("%d: got %q want %q", i, got, c.want)
		}
	}
}

func TestHighlight(t *testing.T) {
	lines := []string{stDim.Render("  ↳ Read file"), "plain"}
	s := selection{a: cell{0, 4}, b: cell{0, 7}}
	out := highlight(lines, s)
	if out[1] != "plain" || lines[0] == out[0] {
		t.Fatalf("%q", out)
	}
	if ansi.Strip(out[0]) != ansi.Strip(lines[0]) {
		t.Fatalf("text changed: %q", ansi.Strip(out[0]))
	}
	if !strings.Contains(out[0], stSelected+"Read") {
		t.Fatalf("no reverse video on the selection: %q", out[0])
	}
	if got := highlight(lines, selection{}); &got[0] != &lines[0] {
		t.Fatal("no selection should return the lines as they are")
	}
}

func TestTUIMouseSelectCopies(t *testing.T) {
	var copied []string
	old := copyFn
	copyFn = func(s string) { copied = append(copied, s) }
	t.Cleanup(func() { copyFn = old })

	m := newModel(&Session{C: &Client{URL: "http://127.0.0.1:1", Token: "x"}},
		[]conductor.Entry{{N: 1, Dir: "in", From: "human", Text: "write tests for tickets"}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	row := -1
	for i, l := range m.content {
		if strings.HasPrefix(ansi.Strip(l), "write tests") {
			row = i - m.vp.YOffset
		}
	}
	if row < 0 {
		t.Fatalf("line not on screen: %q", m.content)
	}
	m.Update(tea.MouseMsg{X: 0, Y: row, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m.Update(tea.MouseMsg{X: 10, Y: row, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	if !strings.Contains(m.View(), stSelected) {
		t.Fatal("selection not drawn")
	}
	_, cmd := m.Update(tea.MouseMsg{X: 10, Y: row, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone})
	if len(copied) != 1 || copied[0] != "write tests" || cmd == nil {
		t.Fatalf("copied %q", copied)
	}
	if !strings.Contains(m.View(), "copied 11 character(s)") {
		t.Fatal("no copied notice")
	}
	// a key press drops the highlight; a plain click copies nothing
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if strings.Contains(m.View(), stSelected) {
		t.Fatal("highlight kept after typing")
	}
	m.Update(tea.MouseMsg{X: 3, Y: row, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m.Update(tea.MouseMsg{X: 3, Y: row, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone})
	if len(copied) != 1 {
		t.Fatalf("a click copied: %q", copied)
	}
}

func TestTUIWordKeysAndEffort(t *testing.T) {
	m := newModel(&Session{C: &Client{URL: "http://127.0.0.1:1", Token: "x"}}, nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.ta.SetValue("one two three")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	if v := m.ta.Value(); v != "one two Xthree" {
		t.Fatalf("ctrl+left: %q", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	if v := m.ta.Value(); v != "one two Xthree!" {
		t.Fatalf("ctrl+right: %q", v)
	}
	m.Update(statusMsg(Status{Mode: "propose", Account: "claude-a", Model: "opus", Effort: "high"}))
	m.Update(stateMsg{State: "idle"})
	if v := m.View(); !strings.Contains(v, "conductor: idle · claude-a opus · effort high") {
		t.Fatalf("footer:\n%s", v)
	}
}
