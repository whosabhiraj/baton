package chat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/whosabhiraj/baton/internal/conductor"
	"github.com/whosabhiraj/baton/internal/proto"
)

// Options for Run.
type Options struct {
	URL, Token string
	In         io.Reader
	Out        io.Writer
	Backlog    int  // history entries shown on attach (default 200)
	ForceLine  bool // line mode even in a terminal
}

// IsTerminal reports whether both r and w are a console.
func IsTerminal(r io.Reader, w io.Writer) bool {
	rf, ok1 := r.(*os.File)
	wf, ok2 := w.(*os.File)
	return ok1 && ok2 && term.IsTerminal(int(rf.Fd())) && term.IsTerminal(int(wf.Fd()))
}

// Run attaches to the conductor until the user quits (or, in line mode, stdin ends).
func Run(ctx context.Context, o Options) error {
	if o.Backlog <= 0 {
		o.Backlog = 200
	}
	s := &Session{C: &Client{URL: o.URL, Token: o.Token}}
	past, last, err := s.C.History(0, o.Backlog)
	if err != nil {
		return fmt.Errorf("cannot read the conductor history: %w", err)
	}
	if !o.ForceLine && IsTerminal(o.In, o.Out) {
		return runTUI(ctx, s, past, last)
	}
	return runLines(ctx, s, past, last, o.In, o.Out)
}

// ---- line mode ----

var linePrefix = map[string]string{
	KindYou: "you> ", KindHub: "baton> ", KindConductor: "conductor> ", KindTool: "  - ",
	KindError: "  ! ", KindMeta: "  . ", KindNotice: "",
}

func formatLine(l Line) string {
	p := linePrefix[l.Kind]
	ind := strings.Repeat(" ", len(p))
	return p + strings.ReplaceAll(l.Text, "\n", "\n"+ind)
}

func runLines(ctx context.Context, s *Session, past []conductor.Entry, last int, in io.Reader, out io.Writer) error {
	var mu sync.Mutex
	print := func(ls []Line) {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range ls {
			fmt.Fprintln(out, formatLine(l))
		}
	}
	for _, e := range past {
		print(Render(e))
	}
	print(notice("chat attached (line mode). /help for commands, /quit or end of input to detach."))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.C.Follow(ctx, last, func(e conductor.Entry) { print(Render(e)) }, nil)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		ls, err := s.Input(sc.Text())
		print(ls)
		if errors.Is(err, ErrQuit) {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	print(notice("detached; the conductor and agents keep running. `baton attach` to come back."))
	return nil
}

// stateText describes the conductor state for the footer.
func stateText(c proto.ConductorState) string {
	if c.State == "" {
		return "conductor: unknown"
	}
	s := "conductor: " + c.State
	if c.Account != "" {
		s += " · " + c.Account
		if c.Model != "" {
			s += " " + c.Model
		}
	}
	return s
}
