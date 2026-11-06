package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/runs"
)

func ocAcct(env map[string]string) *config.Account {
	return &config.Account{ID: "oc", Kind: "opencode", ForbidClaudeSubscription: true, Env: env}
}

func writeAuth(t *testing.T, dir, body string) {
	t.Helper()
	d := filepath.Join(dir, "opencode")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPreflight(t *testing.T) {
	const secret = "PLACEHOLDER-NOT-A-SECRET"
	cases := []struct {
		name    string
		acct    func(dir string) *config.Account
		auth    string // "" = no file
		wantErr string // "" = allowed
	}{
		{"claude kind untouched", func(string) *config.Account { return &config.Account{ID: "c", Kind: "claude"} }, "", ""},
		{"no isolation var", func(string) *config.Account { return ocAcct(nil) }, "", "XDG_DATA_HOME"},
		{"relative isolation var", func(string) *config.Account { return ocAcct(map[string]string{"XDG_DATA_HOME": "rel"}) }, "", "absolute"},
		{"no auth file", func(d string) *config.Account { return ocAcct(map[string]string{"XDG_DATA_HOME": d}) }, "", ""},
		{"api key providers", func(d string) *config.Account { return ocAcct(map[string]string{"XDG_DATA_HOME": d}) },
			`{"anthropic":{"type":"api","key":"` + secret + `"},"openai":{"type":"oauth","refresh":"x","access":"y","expires":1}}`, ""},
		{"anthropic oauth", func(d string) *config.Account { return ocAcct(map[string]string{"XDG_DATA_HOME": d}) },
			`{"anthropic":{"type":"oauth","refresh":"` + secret + `","access":"y","expires":1}}`, "Claude subscription"},
		{"claude-named oauth", func(d string) *config.Account { return ocAcct(map[string]string{"XDG_DATA_HOME": d}) },
			`{"Claude-Max":{"type":"oauth"}}`, "Claude subscription"},
		{"anthropic unknown type", func(d string) *config.Account { return ocAcct(map[string]string{"XDG_DATA_HOME": d}) },
			`{"anthropic":{"type":"wellknown"}}`, "Claude subscription"},
		{"malformed auth fails closed", func(d string) *config.Account { return ocAcct(map[string]string{"XDG_DATA_HOME": d}) }, `{nope`, "not valid JSON"},
		{"oauth token env", func(d string) *config.Account {
			return ocAcct(map[string]string{"XDG_DATA_HOME": d, "claude_code_oauth_token": secret})
		}, "", "CLAUDE_CODE_OAUTH_TOKEN"},
		{"inline auth oauth", func(d string) *config.Account {
			return ocAcct(map[string]string{"XDG_DATA_HOME": d, "OPENCODE_AUTH_CONTENT": `{"anthropic":{"type":"oauth"}}`})
		}, "", "Claude subscription"},
		{"forbid on claude kind needs api key", func(string) *config.Account {
			return &config.Account{ID: "c", Kind: "claude", ForbidClaudeSubscription: true}
		}, "", "ANTHROPIC_API_KEY"},
		{"forbid on claude kind with api key", func(string) *config.Account {
			return &config.Account{ID: "c", Kind: "claude", ForbidClaudeSubscription: true, Env: map[string]string{"ANTHROPIC_API_KEY": secret}}
		}, "", ""},
		{"forbid on codex with oauth env", func(string) *config.Account {
			return &config.Account{ID: "x", Kind: "codex", ForbidClaudeSubscription: true, Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": secret}}
		}, "", "CLAUDE_CODE_OAUTH_TOKEN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.auth != "" {
				writeAuth(t, dir, c.auth)
			}
			err := preflight(c.acct(dir), runs.SpawnResult{})
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaks a credential value: %v", err)
			}
		})
	}
}
