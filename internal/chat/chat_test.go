package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/chat"
	"github.com/whosabhiraj/baton/internal/conductor"
	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/hub"
)

func TestMain(m *testing.M) {
	if os.Getenv(conductor.FakeEnv) == "1" {
		os.Exit(conductor.FakeCLI(os.Args[1:], os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

func out(t *testing.T, v any) conductor.Entry {
	b, _ := json.Marshal(v)
	return conductor.Entry{Dir: "out", Msg: b}
}

func TestRender(t *testing.T) {
	cases := []struct {
		e    conductor.Entry
		want []chat.Line
	}{
		{conductor.Entry{Dir: "in", From: "human", Text: "hi"}, []chat.Line{{Kind: chat.KindYou, Text: "hi"}}},
		{conductor.Entry{Dir: "in", From: "hub", Text: "[baton] x"}, []chat.Line{{Kind: chat.KindHub, Text: "[baton] x"}}},
		{conductor.Entry{Dir: "hub", Event: "exited", Text: "code 1"}, []chat.Line{{Kind: chat.KindError, Text: "conductor exited: code 1"}}},
		{out(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "text", "text": " Plan ready. "},
			map[string]any{"type": "tool_use", "name": "mcp__baton__spawn_agent", "input": map[string]any{
				"task": "long task", "role": "implementer", "account": "claude-b", "plan": []any{}}},
		}}}), []chat.Line{{Kind: chat.KindConductor, Text: "Plan ready."},
			{Kind: chat.KindTool, Text: "spawn_agent account=claude-b role=implementer task=long task"}}},
		{out(t, map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "is_error": true, "content": "ACCOUNT_BUSY: claude-b is at max"}}}}),
			[]chat.Line{{Kind: chat.KindError, Text: "ACCOUNT_BUSY: claude-b is at max"}}},
		{out(t, map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "content": "fine"}}}}), nil},
		{out(t, map[string]any{"type": "result", "subtype": "success", "duration_ms": 2500, "total_cost_usd": 0.0123}),
			[]chat.Line{{Kind: chat.KindMeta, Text: "2.5s · $0.0123"}}},
		{out(t, map[string]any{"type": "result", "subtype": "error_max_turns", "is_error": true}),
			[]chat.Line{{Kind: chat.KindError, Text: "turn failed: error_max_turns"}}},
		{out(t, map[string]any{"type": "system", "subtype": "init"}), nil},
	}
	for i, c := range cases {
		got := chat.Render(c.e)
		if len(got) != len(c.want) {
			t.Fatalf("%d: %+v", i, got)
		}
		for j := range got {
			if got[j] != c.want[j] {
				t.Fatalf("%d: got %+v want %+v", i, got[j], c.want[j])
			}
		}
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

const cfg = `hub:
  mode: propose
conductor:
  auto_wake: false
accounts:
  - id: c1
    kind: claude
    roles: [implementer]
`

func TestLineModeAgainstHub(t *testing.T) {
	d := t.TempDir()
	repo := filepath.Join(d, "repo")
	os.MkdirAll(repo, 0o755)
	if err := gitx.Init(repo); err != nil {
		t.Skip("git:", err)
	}
	gp := filepath.Join(d, "config.yaml")
	os.WriteFile(gp, []byte(cfg), 0o644)
	h, err := hub.Start(context.Background(), hub.Options{DataDir: filepath.Join(d, "data"), AdminToken: "tok",
		GlobalConfig: gp, ProjectConfig: filepath.Join(d, "none.yaml"), Repo: repo, IdleStop: -1,
		ConductorExe: "baton", ConductorCLI: os.Args[0], ConductorEnv: map[string]string{conductor.FakeEnv: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())

	pr, pw := io.Pipe()
	var buf syncBuf
	done := make(chan error, 1)
	go func() {
		done <- chat.Run(context.Background(), chat.Options{URL: h.URL, Token: "tok", In: pr, Out: &buf})
	}()
	waitOut := func(sub string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(buf.String(), sub) {
			if time.Now().After(deadline) {
				t.Fatalf("no %q in:\n%s", sub, buf.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitOut("chat attached")
	io.WriteString(pw, "hello there\n")
	waitOut("conductor> echo: hello there")
	waitOut("you> hello there")
	waitOut("  - list_agents")
	io.WriteString(pw, "/approvals\n/agents\n/mode\n/bogus\n")
	waitOut("no pending approvals")
	waitOut("no live agents")
	waitOut("mode: propose")
	waitOut("unknown command /bogus")
	io.WriteString(pw, "/effort turbo\n/effort medium\n/model sonnet\n")
	waitOut("effort must be one of")
	waitOut("conductor effort is now medium")
	waitOut("conductor model is now sonnet")
	if b, _ := os.ReadFile(gp); !strings.Contains(string(b), "effort: medium") || !strings.Contains(string(b), "model: sonnet") {
		t.Fatalf("config not saved:\n%s", b)
	}
	// the next message restarts the conductor with the new settings (same conversation)
	io.WriteString(pw, "again\n")
	waitOut("sonnet · medium")
	io.WriteString(pw, "/effort\n")
	waitOut("conductor effort: medium")
	io.WriteString(pw, "/quit\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not quit")
	}
	waitOut("detached")

	// a second attach replays the conversation
	var buf2 syncBuf
	if err := chat.Run(context.Background(), chat.Options{URL: h.URL, Token: "tok", In: strings.NewReader(""), Out: &buf2}); err != nil {
		t.Fatal(err)
	}
	if s := buf2.String(); !strings.Contains(s, "you> hello there") || !strings.Contains(s, "conductor> echo: hello there") {
		t.Fatal(s)
	}
}
