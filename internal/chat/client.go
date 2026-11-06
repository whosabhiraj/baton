// Package chat is the terminal side of the conductor chat (`baton`, `baton attach`): a client of
// the hub's conductor routes (PROTOCOL.md A17/A18). It renders the conductor history, sends what
// the user types, and handles a few slash commands (approvals, agents, mode). In a terminal it
// runs a Bubble Tea UI; otherwise (pipes, mintty without a console) a plain line mode.
package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/whosabhiraj/baton/internal/conductor"
	"github.com/whosabhiraj/baton/internal/proto"
)

// Client talks to the hub with the admin token.
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client // nil = default (no proxy); the stream uses its own client without timeout
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}
}

// APIError is a non-2xx hub answer.
type APIError struct {
	Status    int
	Code, Msg string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return e.Code + ": " + e.Msg
}

// Do sends a JSON request; out (may be nil) receives the JSON answer.
func (c *Client) Do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(c.URL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		e := &APIError{Status: resp.StatusCode}
		var m struct{ Code, Error string }
		if json.Unmarshal(b, &m) == nil {
			e.Code, e.Msg = m.Code, m.Error
		}
		return e
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// History is GET /api/conductor/history.
func (c *Client) History(after, limit int) ([]conductor.Entry, int, error) {
	var r struct {
		Entries []conductor.Entry `json:"entries"`
		Last    int               `json:"last"`
	}
	err := c.Do("GET", fmt.Sprintf("/api/conductor/history?after=%d&limit=%d", after, limit), nil, &r)
	return r.Entries, r.Last, err
}

// Send is POST /api/conductor/send.
func (c *Client) Send(text string) error {
	return c.Do("POST", "/api/conductor/send", map[string]string{"text": text}, nil)
}

// Follow streams GET /api/conductor/stream from after, reconnecting (with Last-Event-ID) until
// ctx ends. Callbacks run on the streaming goroutine.
func (c *Client) Follow(ctx context.Context, after int, onEntry func(conductor.Entry), onState func(proto.ConductorState)) {
	last := after
	delay := 500 * time.Millisecond
	cl := &http.Client{Transport: &http.Transport{Proxy: nil}}
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.URL, "/")+"/api/conductor/stream", nil)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Last-Event-ID", strconv.Itoa(last))
		resp, err := cl.Do(req)
		if err == nil && resp.StatusCode == 200 {
			delay = 500 * time.Millisecond
			readSSE(resp.Body, func(ev, data string) {
				switch ev {
				case "entry":
					var e conductor.Entry
					if json.Unmarshal([]byte(data), &e) == nil && e.N > last {
						last = e.N
						onEntry(e)
					}
				case "state":
					var s proto.ConductorState
					if json.Unmarshal([]byte(data), &s) == nil && onState != nil {
						onState(s)
					}
				}
			})
		}
		if resp != nil {
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, 5*time.Second)
	}
}

func readSSE(r io.Reader, fn func(ev, data string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), 8<<20)
	ev, data := "", []string{}
	for sc.Scan() {
		l := sc.Text()
		switch {
		case l == "":
			if len(data) > 0 {
				fn(ev, strings.Join(data, "\n"))
			}
			ev, data = "", data[:0]
		case strings.HasPrefix(l, "event:"):
			ev = strings.TrimSpace(l[6:])
		case strings.HasPrefix(l, "data:"):
			data = append(data, strings.TrimPrefix(l[5:], " "))
		}
	}
}

// Status is the footer: what the hub and the conductor are doing.
type Status struct {
	Conductor proto.ConductorState
	Mode      string
	Live      int // agents not ended
	Approvals int // pending
	Err       string
}

// FetchStatus reads GET /api/state.
func (c *Client) FetchStatus() Status {
	var st struct {
		Mode      string               `json:"mode"`
		Conductor proto.ConductorState `json:"conductor"`
		Approvals []json.RawMessage    `json:"approvals"`
		Runs      []struct {
			Status string `json:"status"`
		} `json:"runs"`
	}
	if err := c.Do("GET", "/api/state", nil, &st); err != nil {
		return Status{Err: err.Error()}
	}
	s := Status{Conductor: st.Conductor, Mode: st.Mode, Approvals: len(st.Approvals)}
	for _, r := range st.Runs {
		switch r.Status {
		case "done", "failed", "killed", "exited":
		default:
			s.Live++
		}
	}
	return s
}
