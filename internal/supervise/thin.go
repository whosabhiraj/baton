package supervise

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"
)

// thinEvery is how often a streaming line (a token delta) is kept: often enough to show the
// agent is alive (thinking, writing), far less than one line per token.
const thinEvery = 5 * time.Second

// thinWriter passes complete lines through to w, except streaming deltas (a JSON line whose
// "type" or "method" ends with one of the suffixes, case-insensitive), of which it keeps one per
// thinEvery. The deltas only matter as a sign of life: the full message follows anyway.
type thinWriter struct {
	w        io.Writer
	suffixes []string
	buf      []byte
	last     time.Time
	now      func() time.Time
}

func newThinWriter(w io.Writer, suffixes []string) *thinWriter {
	low := make([]string, len(suffixes))
	for i, s := range suffixes {
		low[i] = strings.ToLower(s)
	}
	return &thinWriter{w: w, suffixes: low, now: time.Now}
}

func (t *thinWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := t.buf[:i+1]
		if t.keep(line) {
			if _, err := t.w.Write(line); err != nil {
				return 0, err
			}
		}
		t.buf = t.buf[i+1:]
	}
	if len(t.buf) > 4<<20 { // a huge line with no newline yet: don't hold it all
		if _, err := t.w.Write(t.buf); err != nil {
			return 0, err
		}
		t.buf = t.buf[:0]
	}
	return len(p), nil
}

// Flush writes a trailing line without a newline (the process ended mid-line).
func (t *thinWriter) Flush() error {
	if len(t.buf) == 0 {
		return nil
	}
	_, err := t.w.Write(t.buf)
	t.buf = nil
	return err
}

func (t *thinWriter) keep(line []byte) bool {
	low := bytes.ToLower(line)
	if !slices.ContainsFunc(t.suffixes, func(s string) bool { return bytes.Contains(low, []byte(s)) }) {
		return true // cheap pre-check: most lines are not deltas
	}
	var m struct{ Type, Method string }
	if json.Unmarshal(line, &m) != nil {
		return true
	}
	typ, method := strings.ToLower(m.Type), strings.ToLower(m.Method)
	if !slices.ContainsFunc(t.suffixes, func(s string) bool { return strings.HasSuffix(typ, s) || strings.HasSuffix(method, s) }) {
		return true
	}
	if now := t.now(); now.Sub(t.last) >= thinEvery {
		t.last = now
		return true
	}
	return false
}
