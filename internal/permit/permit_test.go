package permit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeHub struct {
	mu        sync.Mutex
	decision  string // "" = never decide
	note      string
	createErr int // non-zero: answer the create with this status
	created   []map[string]any
	cancelled bool
	auth      string
}

func (f *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/runs/r1/access":
		if f.createErr != 0 {
			w.WriteHeader(f.createErr)
			w.Write([]byte(`{"ok":false,"code":"RUN_ENDED"}`))
			return
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.created = append(f.created, b)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "approval": map[string]any{"id": "ap1", "status": "pending"}})
	case r.Method == "GET" && r.URL.Path == "/api/runs/r1/access/ap1":
		if f.decision == "" {
			time.Sleep(20 * time.Millisecond) // a real hub holds the poll; keep the test loop from spinning
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": "pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": f.decision, "note": f.note})
	case r.Method == "POST" && r.URL.Path == "/api/runs/r1/access/ap1/cancel":
		f.cancelled = true
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	default:
		http.Error(w, "nope", 404)
	}
}

func session(t *testing.T, c *Client) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := NewServer(c, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func hubClient(t *testing.T, h *fakeHub, timeout time.Duration) *Client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{URL: srv.URL, Token: "tok", Run: "r1", Timeout: timeout}
}

// call returns the decoded JSON answer.
func call(t *testing.T, cs *mcp.ClientSession, args any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolName, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("bad result %+v", res)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func detailOf(t *testing.T, h *fakeHub) map[string]any {
	t.Helper()
	if len(h.created) != 1 {
		t.Fatalf("created %+v", h.created)
	}
	d, _ := h.created[0]["detail"].(map[string]any)
	return d
}

func TestBashApproved(t *testing.T) {
	h := &fakeHub{decision: "approved"}
	cs := session(t, hubClient(t, h, 5*time.Second))
	in := map[string]any{"command": "ls -la", "description": "list files", "extra": []any{1.0, "x"}}
	out := call(t, cs, map[string]any{"tool_name": "Bash", "input": in, "tool_use_id": "tu1"})
	if out["behavior"] != "allow" || !reflect.DeepEqual(out["updatedInput"], in) {
		t.Fatalf("got %+v", out)
	}
	if h.created[0]["kind"] != "command" || h.created[0]["summary"] != "list files" || h.auth != "Bearer tok" {
		t.Fatalf("hub saw %+v auth %q", h.created, h.auth)
	}
	if d := detailOf(t, h); d["command"] != "ls -la" || d["tool"] != "Bash" {
		t.Fatalf("detail %+v", d)
	}
}

func TestUnsandboxed(t *testing.T) {
	h := &fakeHub{decision: "approved"}
	cs := session(t, hubClient(t, h, 5*time.Second))
	call(t, cs, map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "rm x", "dangerouslyDisableSandbox": true}})
	d := detailOf(t, h)
	if s, _ := d["unsandboxed"].(string); !strings.HasPrefix(s, "yes:") {
		t.Fatalf("detail %+v", d)
	}
	if s, _ := h.created[0]["summary"].(string); s != "outside the sandbox: run: rm x" {
		t.Fatalf("summary %q", s)
	}
}

func TestEditRejectedWithNote(t *testing.T) {
	h := &fakeHub{decision: "rejected", note: "not that file"}
	cs := session(t, hubClient(t, h, 5*time.Second))
	out := call(t, cs, map[string]any{"tool_name": "Edit", "input": map[string]any{"file_path": "/a/b.go"}})
	if out["behavior"] != "deny" || !strings.Contains(out["message"].(string), "not that file") {
		t.Fatalf("got %+v", out)
	}
	if h.created[0]["kind"] != "file_change" || h.created[0]["summary"] != "edit /a/b.go" || detailOf(t, h)["path"] != "/a/b.go" {
		t.Fatalf("hub saw %+v", h.created)
	}
}

func TestUnknownTool(t *testing.T) {
	h := &fakeHub{decision: "approved"}
	cs := session(t, hubClient(t, h, 5*time.Second))
	call(t, cs, map[string]any{"tool_name": "mcp__x__y", "input": map[string]any{"a": 1}})
	d := detailOf(t, h)
	if h.created[0]["kind"] != "tool" || d["input"] != `{"a":1}` || h.created[0]["summary"] != "use mcp__x__y" {
		t.Fatalf("hub saw %+v", h.created)
	}
}

func TestTimeoutCancels(t *testing.T) {
	h := &fakeHub{}
	cs := session(t, hubClient(t, h, 300*time.Millisecond))
	out := call(t, cs, map[string]any{"tool_name": "Write", "input": map[string]any{"file_path": "f"}})
	if out["behavior"] != "deny" || !strings.Contains(out["message"].(string), "Nobody answered") {
		t.Fatalf("got %+v", out)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.cancelled {
		t.Fatal("cancel not posted")
	}
}

func TestNoConfig(t *testing.T) {
	var errb bytes.Buffer
	cs := session(t, &Client{Stderr: &errb})
	out := call(t, cs, map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "x"}})
	if out["behavior"] != "deny" || !strings.Contains(out["message"].(string), "could not ask") {
		t.Fatalf("got %+v", out)
	}
	if !strings.HasPrefix(errb.String(), "baton __permit: ") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestCreateFails(t *testing.T) {
	h := &fakeHub{createErr: 410}
	cs := session(t, hubClient(t, h, 5*time.Second))
	out := call(t, cs, map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "x"}})
	if out["behavior"] != "deny" || !strings.Contains(out["message"].(string), "could not ask") {
		t.Fatalf("got %+v", out)
	}
}

func TestExtraFieldsAndMissingInput(t *testing.T) {
	h := &fakeHub{decision: "approved"}
	cs := session(t, hubClient(t, h, 5*time.Second))
	out := call(t, cs, map[string]any{"tool_name": "Bash", "surprise": map[string]any{"x": 1}})
	if out["behavior"] != "allow" || !reflect.DeepEqual(out["updatedInput"], map[string]any{}) {
		t.Fatalf("got %+v", out)
	}
}

func TestSummarySingleLineAndCap(t *testing.T) {
	h := &fakeHub{decision: "approved"}
	cs := session(t, hubClient(t, h, 5*time.Second))
	call(t, cs, map[string]any{"tool_name": "Bash", "input": map[string]any{
		"command": "x", "description": "line one\r\nline\ttwo\n" + strings.Repeat("é", 500)}})
	s, _ := h.created[0]["summary"].(string)
	if strings.ContainsAny(s, "\r\n\t") || len([]rune(s)) != 300 || !strings.HasPrefix(s, "line one line two ") {
		t.Fatalf("summary %q (%d runes)", s, len([]rune(s)))
	}
}
