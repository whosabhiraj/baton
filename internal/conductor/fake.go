package conductor

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// FakeEnv makes the baton binary act as a fake `claude` conductor (cmd/baton checks it before
// anything else). `baton mock` uses it so the conductor chat works without a model; tests use
// it too. It speaks the stream-json subset the Manager reads.
const FakeEnv = "BATON_FAKE_CLAUDE"

// FakeCLI runs the fake conductor: one reply per user message ("echo: <text>"), a tool call
// line before it, a result line after it. The message "crash" exits with code 3. If
// BATON_FAKE_ARGS names a file, the arguments are appended to it as a JSON line.
func FakeCLI(args []string, stdin io.Reader, stdout io.Writer) int {
	sid := ""
	for i, a := range args {
		if (a == "--session-id" || a == "--resume") && i+1 < len(args) {
			sid = args[i+1]
		}
	}
	if f := os.Getenv("BATON_FAKE_ARGS"); f != "" {
		b, _ := json.Marshal(args)
		if fh, err := os.OpenFile(f, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			_, _ = fh.Write(append(b, '\n'))
			_ = fh.Close()
		}
	}
	out := bufio.NewWriter(stdout)
	emit := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	first, turns := true, 0
	for sc.Scan() {
		var m struct {
			Message struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(sc.Bytes(), &m)
		text := ""
		if len(m.Message.Content) > 0 {
			text = m.Message.Content[0].Text
		}
		if text == "crash" {
			return 3
		}
		start := time.Now()
		if first {
			emit(map[string]any{"type": "system", "subtype": "init", "session_id": sid, "model": "fake"})
			first = false
		}
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": "m1", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "mcp__baton__list_agents", "input": map[string]any{}}}}})
		emit(map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}}}})
		reply := "echo: " + text
		if strings.HasPrefix(text, "[baton]") {
			reply = "echo: [baton] (mock conductor: no model runs here, nothing was handled)"
		}
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": "m2", "content": []any{
			map[string]any{"type": "text", "text": reply}}}})
		turns++ // like claude, modelUsage is a running total for the session; no cost (no quota)
		emit(map[string]any{"type": "result", "subtype": "success", "session_id": sid, "is_error": false,
			"duration_ms": time.Since(start).Milliseconds(), "total_cost_usd": 0.0,
			"usage":      map[string]any{"input_tokens": 1, "output_tokens": 1},
			"modelUsage": map[string]any{"fake": map[string]any{"inputTokens": 10 * turns, "outputTokens": 5 * turns}}})
	}
	return 0
}
