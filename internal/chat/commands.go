package chat

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Help is the /help text.
const Help = `Type a message for the conductor and press Enter. Commands:
  /agents                      live agents
  /approvals                   pending approvals (numbered)
  /approve <n|id> [note]       approve one (a merge is performed now)
  /reject <n|id> [note]        reject one
  /mode [user|propose|auto]    show or switch how lineups are decided
  /model [name]                show or set the conductor's model (e.g. opus, sonnet)
  /effort [level]              show or set its effort (low, medium, high, xhigh, max)
  /start  /stop                start or stop the conductor (agents keep running)
  /reset yes                   stop the conductor and forget the conversation
  /quit                        detach (Ctrl+C too); everything keeps running`

// Efforts are the effort levels /effort accepts (the claude CLI's --effort values).
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// ErrQuit is returned by Command for /quit.
var ErrQuit = errors.New("quit")

// Session is the state shared by both chat modes.
type Session struct {
	C         *Client
	approvals []approval // last /approvals listing, for numbers
}

type approval struct {
	ID     string         `json:"id"`
	Kind   string         `json:"kind"`
	RunID  string         `json:"run_id"`
	Title  string         `json:"title"`
	Detail map[string]any `json:"detail"`
}

// Input handles one line typed by the user: a slash command, or a message for the conductor.
// It returns lines to show locally.
func (s *Session) Input(text string) ([]Line, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	if !strings.HasPrefix(text, "/") {
		if err := s.C.Send(text); err != nil {
			return []Line{{KindError, "not sent: " + err.Error()}}, nil
		}
		return nil, nil // the message comes back through the stream
	}
	return s.Command(text)
}

func notice(format string, a ...any) []Line { return []Line{{KindNotice, fmt.Sprintf(format, a...)}} }
func failed(err error) []Line               { return []Line{{KindError, err.Error()}} }

// Command runs a slash command.
func (s *Session) Command(text string) ([]Line, error) {
	f := strings.Fields(text)
	cmd, args := strings.ToLower(f[0]), f[1:]
	rest := func(i int) string {
		if len(args) <= i {
			return ""
		}
		return strings.Join(args[i:], " ")
	}
	switch cmd {
	case "/help", "/?":
		return notice("%s", Help), nil
	case "/quit", "/exit", "/detach":
		return nil, ErrQuit
	case "/agents":
		return s.agents(), nil
	case "/approvals":
		return s.listApprovals(), nil
	case "/approve", "/reject":
		if len(args) == 0 {
			return notice("usage: %s <n|id> [note]", cmd), nil
		}
		return s.decide(args[0], cmd == "/approve", rest(1)), nil
	case "/mode":
		if len(args) == 0 {
			st := s.C.FetchStatus()
			if st.Err != "" {
				return notice("mode: unknown (%s)", st.Err), nil
			}
			return notice("mode: %s", st.Mode), nil
		}
		if err := s.C.Do("PATCH", "/api/config", map[string]string{"mode": args[0]}, nil); err != nil {
			return failed(err), nil
		}
		return notice("mode is now %s", args[0]), nil
	case "/model", "/effort":
		key := cmd[1:]
		if len(args) == 0 {
			st := s.C.FetchStatus()
			if st.Err != "" {
				return notice("%s: unknown (%s)", key, st.Err), nil
			}
			v := map[string]string{"model": st.Model, "effort": st.Effort}[key]
			if v == "" {
				v = "CLI default"
			}
			return notice("conductor %s: %s", key, v), nil
		}
		v := strings.ToLower(args[0])
		if key == "effort" && !slices.Contains(Efforts, v) {
			return notice("effort must be one of: %s", strings.Join(Efforts, ", ")), nil
		}
		if err := s.C.Do("PATCH", "/api/config", map[string]string{"conductor." + key: v}, nil); err != nil {
			return failed(err), nil
		}
		return notice("conductor %s is now %s (saved to the config; used from your next message)", key, v), nil
	case "/start", "/stop":
		if err := s.C.Do("POST", "/api/conductor/"+cmd[1:], nil, nil); err != nil {
			return failed(err), nil
		}
		return notice("conductor %s requested", map[string]string{"/start": "start", "/stop": "stop"}[cmd]), nil
	case "/reset":
		if rest(0) != "yes" {
			return notice("/reset forgets the whole conversation. Type /reset yes to do it."), nil
		}
		if err := s.C.Do("POST", "/api/conductor/reset", nil, nil); err != nil {
			return failed(err), nil
		}
		return notice("conversation reset; the next message starts a new one"), nil
	}
	return notice("unknown command %s (try /help)", cmd), nil
}

func (s *Session) agents() []Line {
	var r struct {
		Runs []struct {
			AgentID    string            `json:"agent_id"`
			Callsign   string            `json:"callsign"`
			AccountID  string            `json:"account_id"`
			Model      string            `json:"model"`
			Status     string            `json:"status"`
			Plan       []struct{}        `json:"plan"`
			StepStates map[string]string `json:"step_states"`
			OpenQs     []struct{}        `json:"open_questions"`
		} `json:"runs"`
	}
	if err := s.C.Do("GET", "/api/runs?active=true", nil, &r); err != nil {
		return failed(err)
	}
	if len(r.Runs) == 0 {
		return notice("no live agents")
	}
	var b strings.Builder
	for _, x := range r.Runs {
		done := 0
		for _, st := range x.StepStates {
			if st == "done" {
				done++
			}
		}
		name := x.Callsign
		if name == "" {
			name = x.AgentID
		}
		fmt.Fprintf(&b, "%-10s %-9s %s/%s  step %d/%d", name, x.Status, x.AccountID, x.Model, done, len(x.Plan))
		if len(x.OpenQs) > 0 {
			fmt.Fprintf(&b, "  %d question(s)", len(x.OpenQs))
		}
		b.WriteByte('\n')
	}
	return notice("%s", strings.TrimRight(b.String(), "\n"))
}

func (s *Session) fetchApprovals() error {
	var list []approval
	if err := s.C.Do("GET", "/api/approvals?status=pending", nil, &list); err != nil {
		return err
	}
	s.approvals = list
	return nil
}

func (s *Session) listApprovals() []Line {
	if err := s.fetchApprovals(); err != nil {
		return failed(err)
	}
	if len(s.approvals) == 0 {
		return notice("no pending approvals")
	}
	var b strings.Builder
	for i, a := range s.approvals {
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, a.Kind, a.Title)
		if ds, _ := a.Detail["diff_stat"].(string); ds != "" {
			fmt.Fprintf(&b, " (%s)", ds)
		}
		if a.Kind == "lineup" {
			agents, _ := a.Detail["agents"].([]any)
			for j, x := range agents {
				ag, _ := x.(map[string]any)
				str := func(k string) string { v, _ := ag[k].(string); return v }
				steps, _ := ag["plan"].([]any)
				model := str("model")
				if e := str("effort"); e != "" {
					model += "/" + e
				}
				fmt.Fprintf(&b, "\n   %c) %s %s %s: %s (%d steps)", 'a'+rune(j%26), str("account"), str("role"), model, clip(str("task"), 70), len(steps))
			}
		}
		if n, _ := a.Detail["note"].(string); n != "" {
			fmt.Fprintf(&b, "\n   conductor: %s", n)
		}
		fmt.Fprintf(&b, "\n   id %s\n", a.ID)
	}
	b.WriteString("/approve <n> or /reject <n> [note]")
	return notice("%s", b.String())
}

func (s *Session) decide(ref string, approve bool, note string) []Line {
	id := ref
	if n, err := strconv.Atoi(ref); err == nil {
		if s.approvals == nil {
			if err := s.fetchApprovals(); err != nil {
				return failed(err)
			}
		}
		if n < 1 || n > len(s.approvals) {
			return notice("no approval #%d (see /approvals)", n)
		}
		id = s.approvals[n-1].ID
	}
	verb := "reject"
	if approve {
		verb = "approve"
	}
	var out struct {
		Status string `json:"status"`
		Title  string `json:"title"`
		Result struct {
			Spawned []struct {
				Callsign, Account, Role string
			} `json:"spawned"`
			Failed []struct {
				Account, Role, Code, Error string
			} `json:"failed"`
		} `json:"result"`
	}
	if err := s.C.Do("POST", "/api/approvals/"+id+"/"+verb, map[string]string{"note": note}, &out); err != nil {
		return failed(err)
	}
	s.approvals = nil // numbers change
	lines := notice("%s: %s", out.Status, out.Title)
	for _, x := range out.Result.Spawned {
		lines = append(lines, Line{KindNotice, fmt.Sprintf("  spawned %s (%s, %s)", x.Callsign, x.Account, x.Role)})
	}
	for _, x := range out.Result.Failed {
		lines = append(lines, Line{KindError, fmt.Sprintf("not spawned: %s %s: %s %s", x.Account, x.Role, x.Code, x.Error)})
	}
	return lines
}
