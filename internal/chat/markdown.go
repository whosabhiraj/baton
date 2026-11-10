package chat

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// A small Markdown renderer for the conductor's replies in the terminal chat: the subset chat
// replies use (bold, italic, inline code, strike, links, headings, lists, quotes, rules, fenced
// code). Anything else shows as written. Output is wrapped to width; markers are dropped, so a
// mouse copy gives the plain text.

var (
	mdCode    = lipgloss.NewStyle().Foreground(lipgloss.Color("151"))
	mdHead    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	mdMark    = lipgloss.NewStyle().Foreground(lipgloss.Color("244")) // bullets, numbers, quote bars
	mdURL     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	mdRule    = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	mdFence   = regexp.MustCompile("^\\s*(```+|~~~+)")
	mdHeading = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdBullet  = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	mdOrdered = regexp.MustCompile(`^(\s*)(\d{1,3}[.)])\s+(.*)$`)
	mdQuote   = regexp.MustCompile(`^\s{0,3}>\s?(.*)$`)
	mdHR      = regexp.MustCompile(`^\s{0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
)

// renderMarkdown returns text as styled terminal lines, wrapped to width cells.
func renderMarkdown(text string, width int) string {
	width = max(width, 10)
	var out []string
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		if m := mdFence.FindStringSubmatch(ln); m != nil {
			// fenced code: verbatim, indented, hard-wrapped; ends at the matching fence or the end
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), m[1]); i++ {
				code := strings.ReplaceAll(lines[i], "\t", "    ")
				for _, w := range strings.Split(ansi.Hardwrap(code, width-2, true), "\n") {
					out = append(out, "  "+mdCode.Render(w))
				}
			}
			continue
		}
		switch {
		case mdHR.MatchString(ln):
			out = append(out, mdRule.Render(strings.Repeat("─", min(width, 40))))
		case mdHeading.MatchString(ln):
			m := mdHeading.FindStringSubmatch(ln)
			out = append(out, wrapLines(mdHead.Render(plainInline(m[2])), width)...)
		case mdBullet.MatchString(ln) && !mdHR.MatchString(ln):
			m := mdBullet.FindStringSubmatch(ln)
			out = append(out, hanging(indentOf(m[1]), "• ", inline(m[2]), width)...)
		case mdOrdered.MatchString(ln):
			m := mdOrdered.FindStringSubmatch(ln)
			out = append(out, hanging(indentOf(m[1]), m[2]+" ", inline(m[3]), width)...)
		case mdQuote.MatchString(ln):
			m := mdQuote.FindStringSubmatch(ln)
			for _, w := range wrapLines(inline(m[1]), width-2) {
				out = append(out, mdMark.Render("│ ")+w)
			}
		default:
			out = append(out, wrapLines(inline(ln), width)...)
		}
	}
	return strings.Join(out, "\n")
}

// indentOf turns a list item's leading spaces into a nesting indent (2 cells per level).
func indentOf(lead string) int {
	n := len(strings.ReplaceAll(lead, "\t", "    "))
	return min(n/2, 6) * 2
}

// hanging renders a list item: the marker on the first line, continuation lines under the text.
func hanging(indent int, marker, body string, width int) []string {
	pad := strings.Repeat(" ", indent)
	mw := ansi.StringWidth(marker)
	ls := wrapLines(body, width-indent-mw)
	for i := range ls {
		if i == 0 {
			ls[i] = pad + mdMark.Render(marker) + ls[i]
		} else {
			ls[i] = pad + strings.Repeat(" ", mw) + ls[i]
		}
	}
	return ls
}

func wrapLines(s string, width int) []string {
	return strings.Split(ansi.Wrap(s, max(width, 4), ""), "\n")
}

// ---- inline ----

type mdFlags uint8

const (
	fBold mdFlags = 1 << iota
	fItalic
	fStrike
	fCode
	fURL // a link's target, dimmed after its text
)

type seg struct {
	text  string
	flags mdFlags
}

// inline renders one line's inline markup as styled text.
func inline(s string) string {
	var b strings.Builder
	for _, sg := range parseInline(s, 0) {
		b.WriteString(styleFor(sg.flags).Render(sg.text))
	}
	return b.String()
}

// plainInline renders inline markup without styles (for headings, which have their own).
func plainInline(s string) string {
	var b strings.Builder
	for _, sg := range parseInline(s, 0) {
		b.WriteString(sg.text)
	}
	return b.String()
}

func styleFor(f mdFlags) lipgloss.Style {
	st := lipgloss.NewStyle()
	if f&fCode != 0 {
		st = st.Inherit(mdCode)
	}
	if f&fBold != 0 {
		st = st.Bold(true)
	}
	if f&fItalic != 0 {
		st = st.Italic(true)
	}
	if f&fStrike != 0 {
		st = st.Strikethrough(true)
	}
	if f&fURL != 0 {
		st = mdURL
	}
	return st
}

// parseInline splits s into styled segments. Unmatched markers stay as written.
func parseInline(s string, f mdFlags) []seg {
	var out []seg
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, seg{cur.String(), f})
			cur.Reset()
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isPunct(s[i+1]):
			cur.WriteByte(s[i+1])
			i += 2
			continue
		case c == '`':
			n := run(s, i, '`')
			if j := strings.Index(s[i+n:], strings.Repeat("`", n)); j >= 0 {
				flush()
				code := s[i+n : i+n+j]
				if len(code) > 2 && code[0] == ' ' && code[len(code)-1] == ' ' {
					code = code[1 : len(code)-1]
				}
				out = append(out, seg{code, f | fCode})
				i += n + j + n
				continue
			}
			cur.WriteString(s[i : i+n])
			i += n
			continue
		case c == '[':
			if txt, url, n, ok := link(s[i:]); ok {
				flush()
				out = append(out, parseInline(txt, f)...)
				if url != txt {
					out = append(out, seg{" (" + url + ")", fURL})
				}
				i += n
				continue
			}
		case c == '*' || c == '_' || c == '~':
			if seg, n, ok := emphasis(s, i, f); ok {
				flush()
				out = append(out, seg...)
				i += n
				continue
			}
			n := run(s, i, c) // an unmatched run stays as written
			cur.WriteString(s[i : i+n])
			i += n
			continue
		}
		_, sz := utf8.DecodeRuneInString(s[i:])
		cur.WriteString(s[i : i+sz])
		i += sz
	}
	flush()
	return out
}

// emphasis parses **bold**, __bold__, *italic*, _italic_ or ~~strike~~ starting at s[i].
func emphasis(s string, i int, f mdFlags) ([]seg, int, bool) {
	c := s[i]
	n := run(s, i, c)
	if c == '~' && n != 2 {
		return nil, 0, false
	}
	if n > 3 {
		return nil, 0, false
	}
	open := i + n
	if open >= len(s) || s[open] == ' ' {
		return nil, 0, false // "* item" or "a * b" is not emphasis
	}
	if c == '_' && i > 0 && isWord(lastRune(s[:i])) {
		return nil, 0, false // snake_case_names
	}
	marker := strings.Repeat(string(c), n)
	for from := open; from < len(s); {
		j := strings.Index(s[from:], marker)
		if j < 0 {
			return nil, 0, false
		}
		j += from
		end := j + n
		okClose := j > open && s[j-1] != ' ' && run(s, j, c) == n
		if okClose && c == '_' && end < len(s) && isWord(firstRune(s[end:])) {
			okClose = false
		}
		if okClose {
			var add mdFlags
			switch {
			case c == '~':
				add = fStrike
			case n == 1:
				add = fItalic
			case n == 2:
				add = fBold
			default:
				add = fBold | fItalic
			}
			return parseInline(s[open:j], f|add), end - i, true
		}
		from = j + run(s, j, c)
	}
	return nil, 0, false
}

// link parses [text](url) at the start of s; n is the bytes consumed.
func link(s string) (txt, url string, n int, ok bool) {
	close := strings.Index(s, "](")
	if close < 1 || strings.Contains(s[1:close], "]") {
		return "", "", 0, false
	}
	end := strings.IndexByte(s[close+2:], ')')
	if end < 0 {
		return "", "", 0, false
	}
	url = s[close+2 : close+2+end]
	if url == "" || strings.ContainsAny(url, " \t") {
		return "", "", 0, false
	}
	return s[1:close], url, close + 2 + end + 1, true
}

func run(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

func isPunct(c byte) bool { return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0 }

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}
