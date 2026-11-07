package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/mcpserver"
	"github.com/whosabhiraj/baton/internal/runs"
)

const cfg = `hub:
  mode: propose
accounts:
  - id: a1
    kind: generic
    roles: [implementer]
    max_concurrent: 2
`

// noLaunch starts nothing but keeps each run's token, as a real launcher hands it to the agent
// (the conductor itself never sees run tokens).
type noLaunch struct{}

var runTokens sync.Map // run id -> BATON_TOKEN

func (noLaunch) Start(res runs.SpawnResult) error {
	runTokens.Store(res.RunID, res.Env["BATON_TOKEN"])
	return nil
}
func (noLaunch) Kill(string) error { return nil }

func runToken(t *testing.T, runID string) string {
	t.Helper()
	v, ok := runTokens.Load(runID)
	if !ok || v.(string) == "" {
		t.Fatalf("no token for %s", runID)
	}
	return v.(string)
}

func setup(t *testing.T) (*hub.Hub, *mcp.ClientSession) {
	t.Helper()
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t")
	}
	d := t.TempDir()
	repo := filepath.Join(d, "repo")
	os.MkdirAll(repo, 0o755)
	if err := gitx.Init(repo); err != nil {
		t.Skip("git not available:", err)
	}
	gp := filepath.Join(d, "config.yaml")
	os.WriteFile(gp, []byte(cfg), 0o644)
	h, err := hub.Start(context.Background(), hub.Options{DataDir: filepath.Join(d, "data"), AdminToken: "tok",
		GlobalConfig: gp, ProjectConfig: filepath.Join(d, "none.yaml"), Repo: repo, Launcher: noLaunch{}, IdleStop: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(context.Background()) })
	c := &mcpserver.Client{BaseURL: func() (string, error) { return h.URL, nil }, Token: h.MintConductorToken()}
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := mcpserver.NewServer(c, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return h, cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func TestTools(t *testing.T) {
	h, cs := setup(t)
	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range lt.Tools {
		names = append(names, tl.Name)
	}
	want := []string{"answer_question", "get_agent", "get_settings", "kill_agent", "list_accounts", "list_agents", "list_approvals",
		"merge_agent", "pause_agent", "propose_lineup", "read_events", "resume_agent", "review_diff", "send_message", "set_account_enabled",
		"set_mode", "spawn_agent", "stop_agent", "update_plan", "wait_for_events"}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Fatal(names)
	}

	if out, isErr := callTool(t, cs, "list_accounts", nil); isErr || !strings.Contains(out, `"id":"a1"`) {
		t.Fatal(out)
	}
	if out, isErr := callTool(t, cs, "spawn_agent", map[string]any{"account": "", "role": "implementer", "task": "t", "plan": []any{}}); !isErr || !strings.Contains(out, "ACCOUNT_REQUIRED") {
		t.Fatal(out)
	}
	// propose mode: a direct spawn is refused, a lineup waits for the user
	if out, _ := callTool(t, cs, "get_settings", nil); !strings.Contains(out, `"mode":"propose"`) || !strings.Contains(out, `"merge":true`) {
		t.Fatal(out)
	}
	step := []any{map[string]any{"id": "s1", "title": "write", "acceptance": "file exists"}}
	if out, isErr := callTool(t, cs, "spawn_agent", map[string]any{"account": "a1", "role": "implementer", "task": "t", "plan": step}); !isErr || !strings.Contains(out, "LINEUP_REQUIRED") {
		t.Fatal(out)
	}
	if out, isErr := callTool(t, cs, "propose_lineup", map[string]any{"goal": "notes", "agents": []any{
		map[string]any{"account": "a1", "role": "implementer", "task": "t", "plan": step}}}); isErr || !strings.Contains(out, `"status":"pending"`) {
		t.Fatal(out)
	}
	if out, isErr := callTool(t, cs, "set_mode", map[string]any{"mode": "auto"}); isErr {
		t.Fatal(out)
	}
	if out, isErr := callTool(t, cs, "spawn_agent", map[string]any{"account": "a1", "role": "boss", "task": "t", "plan": []any{}}); !isErr || !strings.Contains(out, "ROLE_NOT_ALLOWED") {
		t.Fatal(out)
	}
	out, isErr := callTool(t, cs, "spawn_agent", map[string]any{"account": "a1", "role": "implementer", "task": "write notes",
		"plan": []any{map[string]any{"id": "s1", "title": "write", "acceptance": "file exists"}}})
	if isErr {
		t.Fatal(out)
	}
	var sp struct {
		RunID, AgentID, Worktree string
		Env                      map[string]string // must stay empty: no run token for the conductor
	}
	sp.RunID = jsonField(t, out, "run_id")
	sp.AgentID = jsonField(t, out, "agent_id")
	json.Unmarshal([]byte(out), &struct {
		Worktree *string            `json:"worktree"`
		Env      *map[string]string `json:"env"`
	}{&sp.Worktree, &sp.Env})
	if len(sp.Env) != 0 {
		t.Fatalf("spawn_agent returned the agent env to the conductor: %v", sp.Env)
	}

	// the agent asks, then finishes; the conductor's first wait still sees both (cursor pinned at the first tool call)
	agent(t, h, sp.RunID, sp.AgentID, runToken(t, sp.RunID), "question", `{"qid":"q_1","text":"tabs or spaces?","blocking":true}`)
	out, _ = callTool(t, cs, "wait_for_events", map[string]any{"timeout_s": 2})
	if !strings.Contains(out, `"type":"question"`) || !strings.Contains(out, "tabs or spaces?") {
		t.Fatal(out)
	}
	if out, isErr := callTool(t, cs, "answer_question", map[string]any{"agent_id": sp.AgentID, "qid": "q_1", "text": "spaces"}); isErr {
		t.Fatal(out)
	}
	os.WriteFile(filepath.Join(sp.Worktree, "notes.txt"), []byte("n\n"), 0o644)
	gitx.Run(context.Background(), sp.Worktree, "add", "notes.txt")
	gitx.Run(context.Background(), sp.Worktree, "commit", "-m", "notes")
	agent(t, h, sp.RunID, sp.AgentID, runToken(t, sp.RunID), "done", `{"status":"success","summary":"wrote notes"}`)
	out, _ = callTool(t, cs, "wait_for_events", map[string]any{"timeout_s": 2})
	if !strings.Contains(out, `"type":"done"`) {
		t.Fatal(out)
	}
	if out, _ = callTool(t, cs, "wait_for_events", map[string]any{"timeout_s": 0.2}); !strings.Contains(out, `"timed_out":true`) {
		t.Fatal("events repeated:", out)
	}

	if out, _ := callTool(t, cs, "get_agent", map[string]any{"agent_id": sp.AgentID}); !strings.Contains(out, `"status":"done"`) {
		t.Fatal(out)
	}
	if out, _ := callTool(t, cs, "review_diff", map[string]any{"agent_id": sp.AgentID}); !strings.Contains(out, "notes.txt") {
		t.Fatal(out)
	}
	if out, isErr := callTool(t, cs, "merge_agent", map[string]any{"agent_id": sp.AgentID, "confirm": false}); !isErr || !strings.Contains(out, "CONFIRM_REQUIRED") {
		t.Fatal(out)
	}
	if out, _ := callTool(t, cs, "merge_agent", map[string]any{"agent_id": sp.AgentID, "confirm": true, "note": "lgtm"}); !strings.Contains(out, `"status":"pending"`) {
		t.Fatal(out)
	}
	if out, _ := callTool(t, cs, "list_approvals", nil); !strings.Contains(out, `"kind":"merge"`) || !strings.Contains(out, `"kind":"lineup"`) {
		t.Fatal(out)
	}
	if h.Config().Hub.Mode != "auto" {
		t.Fatal("mode not switched")
	}
	if out, isErr := callTool(t, cs, "set_mode", map[string]any{"mode": "chaos"}); !isErr || !strings.Contains(out, "CONFIG_INVALID") {
		t.Fatal(out)
	}
	if out, _ := callTool(t, cs, "read_events", map[string]any{"agent_id": sp.AgentID, "types": []string{"done"}}); !strings.Contains(out, "wrote notes") {
		t.Fatal(out)
	}
}

func jsonField(t *testing.T, s, k string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(s, err)
	}
	v, _ := m[k].(string)
	return v
}

var msgN int

func agent(t *testing.T, h *hub.Hub, runID, agentID, tok, typ, payload string) {
	t.Helper()
	msgN++
	body := fmt.Sprintf(`{"v":1,"run_id":%q,"agent_id":%q,"type":%q,"msg_id":"m%d","payload":%s}`, runID, agentID, typ, msgN, payload)
	req, _ := http.NewRequest("POST", h.URL+"/api/ingest", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(typ, resp.StatusCode)
	}
}
