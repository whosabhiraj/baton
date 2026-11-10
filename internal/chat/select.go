package chat

import (
	"os"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// Mouse selection in the conversation: drag to select, release to copy (like the terminal's own
// selection, which mouse mode turns off). Positions are in content lines and screen cells.

type cell struct{ line, col int }

type selection struct {
	dragging bool // left button held
	a, b     cell // anchor (press) and the current end
}

// active reports whether something is selected (not just a click).
func (s selection) active() bool { return s.a != s.b }

// ordered returns the two ends with the earlier one first.
func (s selection) ordered() (cell, cell) {
	if s.b.line < s.a.line || (s.b.line == s.a.line && s.b.col < s.a.col) {
		return s.b, s.a
	}
	return s.a, s.b
}

// span is the cell range [from, to) a selection covers on line i (to = -1: to the end).
func (s selection) span(i int) (from, to int, ok bool) {
	start, end := s.ordered()
	if i < start.line || i > end.line {
		return 0, 0, false
	}
	to = -1
	if i == start.line {
		from = start.col
	}
	if i == end.line {
		to = end.col + 1 // the cell under the pointer is included
	}
	return from, to, true
}

// selectedText is the plain text of the selection: escape codes stripped, trailing padding
// trimmed on each line.
func selectedText(lines []string, s selection) string {
	if !s.active() {
		return ""
	}
	start, end := s.ordered()
	var out []string
	for i := max(start.line, 0); i <= end.line && i < len(lines); i++ {
		plain := ansi.Strip(lines[i])
		from, to, _ := s.span(i)
		if to < 0 {
			to = ansi.StringWidth(plain)
		}
		out = append(out, strings.TrimRight(ansi.Cut(plain, from, to), " "))
	}
	return strings.Join(out, "\n")
}

const stSelected = "\x1b[7m" // reverse video

// highlight returns lines with the selection drawn in reverse video. Lines outside it are
// returned as they are; selected parts lose their colours while selected.
func highlight(lines []string, s selection) []string {
	if !s.active() {
		return lines
	}
	out := make([]string, len(lines))
	copy(out, lines)
	start, end := s.ordered()
	for i := max(start.line, 0); i <= end.line && i < len(lines); i++ {
		from, to, _ := s.span(i)
		w := ansi.StringWidth(lines[i])
		if to < 0 || to > w {
			to = w
		}
		if from >= to {
			continue
		}
		mid := ansi.Strip(ansi.Cut(lines[i], from, to))
		out[i] = ansi.Cut(lines[i], 0, from) + ansi.ResetStyle + stSelected + mid + ansi.ResetStyle + ansi.Cut(lines[i], to, w)
	}
	return out
}

// copyFn is what a finished selection is copied with (tests swap it).
var copyFn = copyText

// copyText puts text on the system clipboard; where there is none (SSH, a headless Linux), it
// asks the terminal to do it (OSC 52, supported by most modern terminals).
func copyText(text string) {
	if os.Getenv("SSH_TTY") == "" && os.Getenv("SSH_CONNECTION") == "" && clipboard.WriteAll(text) == nil {
		return
	}
	termenv.NewOutput(os.Stdout).Copy(text)
}
