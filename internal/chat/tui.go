package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/whosabhiraj/baton/internal/conductor"
	"github.com/whosabhiraj/baton/internal/proto"
)

// Muted palette (the dashboard's: quiet, amber only for "needs you").
var (
	stYouTag   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	stCondTag  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("110"))
	stHubTag   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("174"))
	stNotice   = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	stAmber    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stFooter   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	stRule     = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	stInputBox = lipgloss.NewStyle().PaddingLeft(1)
)

type (
	entryMsg  conductor.Entry
	stateMsg  proto.ConductorState
	statusMsg Status
	tickMsg   struct{}
	localMsg  []Line
	flashMsg  struct{} // the footer notice has expired
)

type model struct {
	s      *Session
	vp     viewport.Model
	ta     textarea.Model
	lines  []Line
	status Status
	cond   proto.ConductorState
	width  int
	height int
	ready  bool
	quit   bool

	content    []string  // rendered conversation lines (what the viewport shows, unhighlighted)
	sel        selection // mouse selection in content
	flash      string    // short footer notice ("copied ...")
	flashUntil time.Time
}

const inputHeight = 3

func newModel(s *Session, past []conductor.Entry) *model {
	ta := textarea.New()
	ta.Placeholder = "Message the conductor (Enter sends, Alt+Enter new line, /help)"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.SetHeight(inputHeight)
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	ta.KeyMap.WordForward = key.NewBinding(key.WithKeys("ctrl+right", "alt+right", "alt+f"))
	ta.KeyMap.WordBackward = key.NewBinding(key.WithKeys("ctrl+left", "alt+left", "alt+b"))
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Focus()
	m := &model{s: s, ta: ta}
	for _, e := range past {
		m.lines = append(m.lines, Render(e)...)
	}
	m.lines = append(m.lines, Line{KindNotice, "attached. /help for commands, Ctrl+C to detach (everything keeps running)."})
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.fetchStatus())
}

func (m *model) fetchStatus() tea.Cmd {
	return func() tea.Msg { return statusMsg(m.s.C.FetchStatus()) }
}

func tick() tea.Cmd { return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) render() string {
	w := max(m.width-2, 20)
	body := lipgloss.NewStyle().Width(w)
	var b strings.Builder
	prevKind := ""
	for i, l := range m.lines {
		// the conductor's turn starts with its first text or tool line after a message
		turn := (l.Kind == KindConductor || l.Kind == KindTool) && (prevKind == KindYou || prevKind == KindHub || prevKind == "")
		if i > 0 && (l.Kind == KindYou || l.Kind == KindHub || turn) {
			b.WriteByte('\n')
		}
		if turn {
			b.WriteString(stCondTag.Render("conductor") + "\n")
		}
		switch l.Kind {
		case KindYou:
			b.WriteString(stYouTag.Render("you") + "\n" + body.Render(l.Text))
		case KindHub:
			b.WriteString(stHubTag.Render("baton · " + clip(l.Text, w-8)))
		case KindConductor:
			b.WriteString(body.Render(l.Text))
		case KindTool:
			b.WriteString(stDim.Render("  ↳ " + clip(l.Text, w-4)))
		case KindError:
			b.WriteString(stErr.Render(lipgloss.NewStyle().Width(w).Render("  ! " + l.Text)))
		case KindMeta:
			b.WriteString(stDim.Render("  · " + clip(l.Text, w-4)))
		case KindNotice:
			b.WriteString(stNotice.Render(body.Render(l.Text)))
		}
		b.WriteByte('\n')
		prevKind = l.Kind
	}
	return b.String()
}

func (m *model) refresh() {
	if !m.ready {
		return
	}
	bottom := m.vp.AtBottom()
	m.setContent()
	if bottom {
		m.vp.GotoBottom()
	}
}

// setContent re-renders the conversation into the viewport, with the selection drawn on it.
func (m *model) setContent() {
	m.content = strings.Split(m.render(), "\n")
	m.paint()
}

// paint redraws the selection without re-rendering the conversation.
func (m *model) paint() {
	m.vp.SetContent(strings.Join(highlight(m.content, m.sel), "\n"))
}

// mouse handles the conversation's mouse events: the wheel scrolls, a left drag selects, and
// releasing the button copies the selection.
func (m *model) mouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		var c tea.Cmd
		m.vp, c = m.vp.Update(msg)
		return c
	}
	at := func(drag bool) cell {
		y := msg.Y
		switch {
		case !drag:
		case y <= 0 && m.vp.YOffset > 0: // dragging onto the top or bottom row scrolls
			m.vp.ScrollUp(1)
		case y >= m.vp.Height-1:
			m.vp.ScrollDown(1)
		}
		y = min(max(y, 0), m.vp.Height-1)
		return cell{line: m.vp.YOffset + y, col: max(msg.X, 0)}
	}
	switch {
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		if msg.Y >= m.vp.Height {
			return nil // the input and footer: nothing to select there
		}
		c := at(false)
		m.sel = selection{dragging: true, a: c, b: c}
		m.paint()
	case msg.Action == tea.MouseActionMotion && m.sel.dragging:
		m.sel.b = at(true)
		m.paint()
	case msg.Action == tea.MouseActionRelease && m.sel.dragging:
		m.sel.dragging = false
		if text := selectedText(m.content, m.sel); text != "" {
			copyFn(text)
			m.flash = fmt.Sprintf("copied %d character(s)", len([]rune(text)))
			m.flashUntil = time.Now().Add(3 * time.Second)
			return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return flashMsg{} })
		}
	}
	return nil
}

func (m *model) footer() string {
	cond := m.cond
	if cond.State == "" {
		cond = m.status.Conductor
	}
	if cond.Account == "" {
		cond.Account = m.status.Account
	}
	if cond.Model == "" {
		cond.Model = m.status.Model
	}
	who := stateText(cond)
	if m.status.Effort != "" && cond.State != "" {
		who += " · effort " + m.status.Effort
	}
	parts := []string{who}
	if m.status.Mode != "" {
		parts = append(parts, "mode "+m.status.Mode)
	}
	parts = append(parts, fmt.Sprintf("%d agent(s)", m.status.Live))
	left := stFooter.Render(strings.Join(parts, " · "))
	if m.status.Approvals > 0 {
		left += stFooter.Render(" · ") + stAmber.Render(fmt.Sprintf("%d approval(s) pending: /approvals", m.status.Approvals))
	}
	if m.status.Err != "" {
		left += stFooter.Render(" · ") + stErr.Render("hub: "+clip(m.status.Err, 60))
	}
	if m.flash != "" && time.Now().Before(m.flashUntil) {
		left += stFooter.Render(" · ") + stNotice.Render(m.flash)
	}
	return left
}

func (m *model) layout() {
	vh := max(m.height-inputHeight-3, 3) // rule + input + rule + footer
	if !m.ready {
		m.vp = viewport.New(m.width, vh)
		m.vp.KeyMap = viewport.KeyMap{ // only page keys: the arrows belong to the input
			PageDown:     key.NewBinding(key.WithKeys("pgdown")),
			PageUp:       key.NewBinding(key.WithKeys("pgup")),
			HalfPageUp:   key.NewBinding(key.WithKeys("ctrl+u")),
			HalfPageDown: key.NewBinding(key.WithKeys("ctrl+d")),
		}
		m.ready = true
	} else {
		m.vp.Width, m.vp.Height = m.width, vh
	}
	m.ta.SetWidth(max(m.width-2, 10))
	m.sel = selection{} // the lines rewrap: old positions mean nothing now
	m.setContent()
	m.vp.GotoBottom()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyMsg:
		if m.sel.active() { // typing drops the highlight (the text stays copied)
			m.sel = selection{}
			m.paint()
		}
		switch msg.String() {
		case "ctrl+c":
			m.quit = true
			return m, tea.Quit
		case "enter":
			text := m.ta.Value()
			m.ta.Reset()
			if strings.TrimSpace(text) == "" {
				return m, nil
			}
			s := m.s
			return m, func() tea.Msg {
				ls, err := s.Input(text)
				if errors.Is(err, ErrQuit) {
					return tea.QuitMsg{}
				}
				return localMsg(ls)
			}
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			var c tea.Cmd
			m.vp, c = m.vp.Update(msg)
			return m, c
		}
	case tea.MouseMsg:
		return m, m.mouse(msg)
	case flashMsg:
		return m, nil // re-render drops the expired notice
	case entryMsg:
		m.lines = append(m.lines, Render(conductor.Entry(msg))...)
		m.refresh()
		if msg.Dir == "out" {
			cmds = append(cmds, m.fetchStatus()) // tool calls change agents/approvals
		}
		return m, tea.Batch(cmds...)
	case stateMsg:
		m.cond = proto.ConductorState(msg)
		return m, nil
	case statusMsg:
		m.status = Status(msg)
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.fetchStatus(), tick())
	case localMsg:
		if len(msg) > 0 {
			m.lines = append(m.lines, msg...)
			m.refresh()
			m.vp.GotoBottom()
		}
		return m, m.fetchStatus()
	}
	var c tea.Cmd
	m.ta, c = m.ta.Update(msg)
	return m, c
}

func (m *model) View() string {
	if !m.ready {
		return "connecting..."
	}
	rule := stRule.Render(strings.Repeat("─", max(m.width, 1)))
	return m.vp.View() + "\n" + rule + "\n" + stInputBox.Render(m.ta.View()) + "\n" + rule + "\n" + m.footer()
}

func runTUI(ctx context.Context, s *Session, past []conductor.Entry, last int) error {
	m := newModel(s, past)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.C.Follow(fctx, last, func(e conductor.Entry) { p.Send(entryMsg(e)) },
		func(st proto.ConductorState) { p.Send(stateMsg(st)) })
	go func() { p.Send(tickMsg{}) }()
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
		err = nil
	}
	fmt.Println("detached; the conductor and agents keep running. `baton attach` to come back.")
	return err
}
