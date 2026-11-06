package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/paths"
)

type inboxMsg struct {
	n   int
	typ string
	p   map[string]any
}

func (c *ctx) inboxDir() string { return filepath.Join(c.env.dir, paths.InboxDir) }
func (c *ctx) readFile() string { return filepath.Join(c.inboxDir(), paths.InboxReadFile) }

func (c *ctx) readSet() map[int]bool {
	set := map[int]bool{}
	b, err := os.ReadFile(c.readFile())
	if err != nil {
		return set
	}
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			set[n] = true
		}
	}
	return set
}

func (c *ctx) markRead(ns ...int) {
	if len(ns) == 0 {
		return
	}
	if os.MkdirAll(c.inboxDir(), 0o755) != nil {
		return
	}
	var sb strings.Builder
	for _, n := range ns {
		sb.WriteString(strconv.Itoa(n))
		sb.WriteByte('\n')
	}
	f, err := os.OpenFile(c.readFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(sb.String())
	_ = f.Close()
}

func decodeObj(b []byte) map[string]any {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var m map[string]any
	if d.Decode(&m) != nil {
		return nil
	}
	return m
}

func toMsg(m map[string]any) (inboxMsg, bool) {
	if m == nil {
		return inboxMsg{}, false
	}
	nn, ok := m["n"].(json.Number)
	typ, ok2 := m["type"].(string)
	if !ok || !ok2 {
		return inboxMsg{}, false
	}
	f, err := nn.Float64()
	if err != nil {
		return inboxMsg{}, false
	}
	p, _ := m["payload"].(map[string]any)
	if p == nil {
		p = map[string]any{}
	}
	return inboxMsg{n: int(f), typ: typ, p: p}, true
}

// loadInbox returns all inbox messages sorted by n: from local files when the run dir exists,
// else from the hub API.
func (c *ctx) loadInbox() []inboxMsg {
	var raw []map[string]any
	if _, err := os.Stat(c.env.dir); err == nil {
		ents, _ := os.ReadDir(c.inboxDir())
		for _, e := range ents {
			if _, ok := paths.ParseInboxFileName(e.Name()); !ok {
				continue
			}
			b, err := os.ReadFile(filepath.Join(c.inboxDir(), e.Name()))
			if err != nil {
				continue
			}
			raw = append(raw, decodeObj(b))
		}
	} else if c.env.url != "" && c.env.token != "" {
		req, err := http.NewRequest(http.MethodGet, c.env.url+"/api/runs/"+c.env.runID+"/inbox?after=0", nil)
		if err == nil {
			req.Header.Set("authorization", "Bearer "+c.env.token)
			if res, err := c.client.Do(req); err == nil {
				b, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
				res.Body.Close()
				if res.StatusCode >= 200 && res.StatusCode < 300 {
					if body := decodeObj(b); body != nil {
						arr, _ := body["messages"].([]any)
						for _, x := range arr {
							m, _ := x.(map[string]any)
							raw = append(raw, m)
						}
					}
				}
			}
		}
	}
	var out []inboxMsg
	for _, m := range raw {
		if im, ok := toMsg(m); ok {
			out = append(out, im)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].n < out[j].n })
	return out
}

var nlSpace = regexp.MustCompile("[\\s\\v\\p{Z}\\x{FEFF}]*\\n[\\s\\v\\p{Z}\\x{FEFF}]*")

func describe(m inboxMsg) string {
	p := m.p
	switch m.typ {
	case "answer":
		return fmt.Sprintf("[%d] answer %s: %s", m.n, jsv(p, "qid"), jsv(p, "text"))
	case "message":
		return fmt.Sprintf("[%d] message (%s): %s", m.n, jsv(p, "from"), jsv(p, "text"))
	case "plan_update":
		steps, _ := p["steps"].([]any)
		parts := make([]string, len(steps))
		for i, s := range steps {
			sm, _ := s.(map[string]any)
			parts[i] = jsv(sm, "id") + " " + jsv(sm, "title")
		}
		return fmt.Sprintf("[%d] plan_update: %s", m.n, strings.Join(parts, "; "))
	}
	if jsTruthy(p, "reason") {
		return fmt.Sprintf("[%d] %s: %s", m.n, m.typ, jsv(p, "reason"))
	}
	return fmt.Sprintf("[%d] %s", m.n, m.typ)
}

func (c *ctx) cmdInbox() int {
	read := c.readSet()
	var ns []int
	var lines []string
	for _, m := range c.loadInbox() {
		if !read[m.n] {
			ns = append(ns, m.n)
			lines = append(lines, nlSpace.ReplaceAllString(describe(m), " "))
		}
	}
	if len(lines) == 0 {
		fmt.Fprintln(c.stdout, "inbox empty")
	}
	for _, l := range lines {
		fmt.Fprintln(c.stdout, l)
	}
	c.markRead(ns...)
	return 0
}

func (c *ctx) cmdAsk(text string, o opts) (int, error) {
	qid := ids.QID()
	p := payload{"qid": qid, "text": text, "blocking": o.flag("blocking")}
	if s := o.get("options"); s != "" {
		opts := []any{}
		for _, x := range strings.Split(s, ",") {
			if x = strings.TrimSpace(x); x != "" {
				opts = append(opts, x)
			}
		}
		p["options"] = opts
	}
	var timeoutS float64
	if o.has("timeout") {
		t := strings.TrimSpace(o.get("timeout"))
		timeoutS = num(t, -1)
		if timeoutS < 0 {
			return 0, usagef("--timeout must be a number of seconds")
		}
	} else {
		timeoutS = num(c.getenv("BATON_ASK_TIMEOUT_S"), 900)
	}
	code, err := c.send("question", p, true)
	if err != nil {
		return 0, err
	}
	if !o.flag("blocking") {
		if code == 0 {
			fmt.Fprintf(c.stdout, "qid %s\n", qid)
		}
		return code, nil
	}
	if code != 0 {
		return code, nil
	}
	dur := func(sec float64) time.Duration {
		if sec > 3e9 {
			sec = 3e9
		}
		return time.Duration(sec * float64(time.Second))
	}
	pollD := max(10*time.Millisecond, dur(num(c.getenv("BATON_POLL_INTERVAL_S"), 2)))
	hbD := max(10*time.Millisecond, dur(num(c.getenv("BATON_HEARTBEAT_INTERVAL_S"), 30)))
	start := time.Now()
	deadline := start.Add(dur(timeoutS))
	lastHb := start
	for {
		read := c.readSet()
		for _, m := range c.loadInbox() {
			if m.typ == "answer" && !read[m.n] && m.p["qid"] == qid {
				c.markRead(m.n)
				t := ""
				if v, ok := m.p["text"]; ok && v != nil {
					t = jsStr(v)
				}
				fmt.Fprintln(c.stdout, t)
				return 0, nil
			}
		}
		left := time.Until(deadline)
		if left <= 0 {
			fmt.Fprintln(c.stdout, "NO_ANSWER")
			return 0, nil
		}
		if time.Since(lastHb) >= hbD {
			lastHb = time.Now()
			if _, err := c.send("heartbeat", payload{}, true); err != nil {
				return 0, err
			}
		}
		time.Sleep(min(pollD, max(time.Millisecond, left)))
	}
}
