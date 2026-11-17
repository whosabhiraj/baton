package adapters

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
)

// Names shared with internal/permit (`baton __permit`, the MCP server that turns claude's
// permission prompts into access requests). adapters does not import permit; a test checks
// that they match.
const (
	PermitServer = "baton_access"
	PermitTool   = "mcp__baton_access__approve"
	PermitConfig = "access-mcp.json" // written into AskVia.PrivateDir
)

// AskVia says how an agent's permission prompts reach the user: `baton __permit` files each
// one as an access request (PROTOCOL.md A20, A21) and hands the decision back to the CLI.
type AskVia struct {
	Driver     string  // the baton binary (runs `baton __permit`)
	HubURL     string  // hub base URL
	HubToken   string  // the run's token: only in the private MCP config, never in the agent's env
	RunID      string  //
	PrivateDir string  // a folder the agent cannot read (outside its worktree and run dir)
	TimeoutS   float64 // how long a prompt waits for the user (the run's ask timeout); 0 = 900
}

func (a AskVia) ok() bool {
	return a.Driver != "" && a.HubURL != "" && a.HubToken != "" && a.RunID != "" && a.PrivateDir != ""
}

// Prompter is implemented by adapters whose CLI can hand its permission prompts to Baton.
// PromptPlan returns the extra flags (Argv), the files for AskVia.PrivateDir (Private) and
// extra environment for the CLI (Env, nothing secret).
type Prompter interface {
	PromptPlan(a AskVia) (SandboxPlan, error)
}

// PromptPlan for claude: --permission-prompt-tool names the tool of the `baton __permit` MCP
// server, whose config (with the run token) lives in the private folder.
func (claude) PromptPlan(a AskVia) (SandboxPlan, error) {
	if !a.ok() {
		return SandboxPlan{}, errors.New("asking the user needs the hub URL, the run token and a private folder")
	}
	timeout := a.TimeoutS
	if timeout <= 0 {
		timeout = 900
	}
	env := map[string]string{"BATON_PERMIT_URL": a.HubURL, "BATON_PERMIT_TOKEN": a.HubToken,
		"BATON_PERMIT_RUN": a.RunID, "BATON_PERMIT_TIMEOUT_S": strconv.FormatFloat(timeout, 'f', -1, 64)}
	cfg := map[string]any{"mcpServers": map[string]any{
		PermitServer: map[string]any{"command": a.Driver, "args": []string{"__permit"}, "env": env},
	}}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return SandboxPlan{}, err
	}
	return SandboxPlan{
		Argv: []string{"--permission-prompts", "host", "--permission-prompt-tool", PermitTool,
			"--mcp-config", filepath.Join(a.PrivateDir, PermitConfig)},
		Private: map[string][]byte{PermitConfig: b},
		// claude waits on the tool while the user decides: give it the whole ask timeout
		Env: map[string]string{"MCP_TOOL_TIMEOUT": strconv.Itoa(int((timeout + 60) * 1000))},
	}, nil
}
