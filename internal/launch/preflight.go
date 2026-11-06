package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/proc"
	"github.com/whosabhiraj/baton/internal/runs"
)

// OpenCode keeps provider logins in <XDG_DATA_HOME>/opencode/auth.json (default
// ~/.local/share), so XDG_DATA_HOME is the per-account isolation variable. (OPENCODE_CONFIG_DIR
// only adds a config directory.) OPENCODE_AUTH_CONTENT overrides the file with inline JSON.
const (
	opencodeIsoVar  = "XDG_DATA_HOME"
	opencodeAuthEnv = "OPENCODE_AUTH_CONTENT"
)

// subscriptionEnv are env vars that carry a Claude subscription credential.
var subscriptionEnv = []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN"}

// preflight refuses a launch before anything is spawned (non-negotiable: OpenCode and any
// forbid_claude_subscription account never run on a Claude subscription). Decisions use only
// the structure of the credentials (provider name and type); values are never read into
// messages or logs.
func preflight(acct *config.Account, _ runs.SpawnResult) error {
	if acct == nil || (acct.Kind != "opencode" && !acct.ForbidClaudeSubscription) {
		return nil
	}
	env := envMap(proc.BuildEnv(nil, acct.Env, nil))
	for _, k := range subscriptionEnv {
		if env[strings.ToUpper(k)] != "" {
			return fmt.Errorf("refusing to start account %q: %s is set (Claude subscriptions are not allowed here)", acct.ID, k)
		}
	}
	if acct.Kind == "claude" && env["ANTHROPIC_API_KEY"] == "" {
		return fmt.Errorf("refusing to start account %q: forbid_claude_subscription needs ANTHROPIC_API_KEY in its env", acct.ID)
	}
	if acct.Kind != "opencode" {
		return nil
	}
	if inline := env[opencodeAuthEnv]; inline != "" {
		if err := checkOpencodeAuth(acct.ID, []byte(inline), opencodeAuthEnv); err != nil {
			return err
		}
	}
	dir := env[opencodeIsoVar]
	if dir == "" {
		return fmt.Errorf("refusing to start account %q: no %s in its env, so OpenCode would use your global login; set it (for example %s: ~/.baton/accounts/%s)",
			acct.ID, opencodeIsoVar, opencodeIsoVar, acct.ID)
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("refusing to start account %q: %s must be an absolute path", acct.ID, opencodeIsoVar)
	}
	path := filepath.Join(dir, "opencode", "auth.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // no stored logins (API keys may come from the env)
	}
	if err != nil {
		return fmt.Errorf("refusing to start account %q: cannot read %s: %v", acct.ID, path, err)
	}
	return checkOpencodeAuth(acct.ID, b, path)
}

// checkOpencodeAuth fails closed: unparsable auth, or an Anthropic/Claude-named provider
// whose entry is anything but an API key (type "api"), is refused.
func checkOpencodeAuth(id string, data []byte, where string) error {
	var auth map[string]json.RawMessage
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("refusing to start account %q: %s is not valid JSON", id, where)
	}
	for provider, raw := range auth {
		p := strings.ToLower(provider)
		if !strings.Contains(p, "anthropic") && !strings.Contains(p, "claude") {
			continue
		}
		var e struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Type != "api" {
			return fmt.Errorf("refusing to start account %q: %s holds a non-API-key login for provider %q (a Claude subscription); log it out (opencode auth logout) or use an API key",
				id, where, provider)
		}
	}
	return nil
}

func envMap(kv []string) map[string]string {
	m := map[string]string{}
	for _, e := range kv {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[strings.ToUpper(k)] = v
		}
	}
	return m
}
