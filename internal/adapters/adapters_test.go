package adapters

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/proto"
)

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
}

func TestClaudeArgv(t *testing.T) {
	base := []string{"claude", "-p", "--output-format", "stream-json", "--verbose"}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	cases := []struct {
		name          string
		acct          config.Account
		model, effort string
		want          []string
	}{
		{"empty mode is permissive", config.Account{}, "", "", cat(base, []string{"--permission-mode", "bypassPermissions"})},
		{"permissive+model+effort", config.Account{Permissions: config.Permissions{Mode: "permissive"}}, "opus", "high",
			cat(base, []string{"--model", "opus", "--effort", "high", "--permission-mode", "bypassPermissions"})},
		{"acceptEdits", config.Account{Permissions: config.Permissions{Mode: "acceptEdits"}}, "default", "",
			cat(base, []string{"--permission-mode", "acceptEdits"})},
		{"default mode no flag", config.Account{Permissions: config.Permissions{Mode: "default"}}, "", "", base},
		{"tools and extra", config.Account{Permissions: config.Permissions{Mode: "default", AllowedTools: []string{"Bash", "Edit"},
			DeniedTools: []string{"WebFetch"}, ExtraArgs: []string{"--max-turns", "9"}}}, "", "",
			cat(base, []string{"--allowedTools", "Bash,Edit", "--disallowedTools", "WebFetch", "--max-turns", "9"})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := For("claude", config.Config{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.Argv(c.acct, c.model, c.effort)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, got, c.want)
		})
	}
}

func TestFor(t *testing.T) {
	cfg := config.Config{Kinds: map[string]config.CustomKind{
		"mine": {Command: "mytool", Args: []string{"--x"}, Parser: "jsonl", Env: map[string]string{"A": "1"}},
		"bad":  {},
	}}
	if _, err := For("generic", cfg); err == nil || !strings.Contains(err.Error(), "kinds.generic") {
		t.Errorf("generic: %v", err)
	}
	if _, err := For("nope", cfg); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := For("bad", cfg); err == nil {
		t.Error("kind without command accepted")
	}
	a, err := For("mine", cfg)
	if err != nil {
		t.Fatal(err)
	}
	argv, _ := a.Argv(config.Account{Permissions: config.Permissions{ExtraArgs: []string{"-y"}}}, "m", "e")
	eq(t, argv, []string{"mytool", "--x", "-y"})
	if a.(EnvProvider).Env()["A"] != "1" {
		t.Error("env")
	}
	evs := a.Parser().Parse([]byte(`{"type":"tick"}`))
	if len(evs) != 1 || evs[0].Payload["detail"] != "tick" {
		t.Errorf("jsonl: %v", evs)
	}
	if evs := a.Parser().Parse([]byte(`not json`)); evs != nil {
		t.Errorf("jsonl garbage: %v", evs)
	}
	cfg.Kinds["n"] = config.CustomKind{Command: "x", Parser: "none"}
	n, _ := For("n", cfg)
	if n.Parser().Parse([]byte(`{"type":"a"}`)) != nil {
		t.Error("none parser emitted")
	}
}

type goldenEvent struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

func TestClaudeParserGolden(t *testing.T) {
	f, err := os.Open("../../protocol/fixtures/adapters/claude-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := newClaudeParser()
	var got []goldenEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, e := range p.Parse(sc.Bytes()) {
			if e.Type == Finished { // launcher-only signal, never stored
				got = append(got, goldenEvent{e.Type, e.Payload})
				continue
			}
			// every payload must satisfy the hub schema
			if _, perr := proto.ValidateHubPayload(e.Type, e.Payload); perr != nil {
				t.Fatalf("%s payload invalid: %v (%v)", e.Type, perr, e.Payload)
			}
			got = append(got, goldenEvent{e.Type, e.Payload})
		}
	}
	// normalise numbers the way JSON does
	b, _ := json.MarshalIndent(got, "", "  ")
	const golden = "../../protocol/fixtures/adapters/claude-stream.golden.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		_ = os.WriteFile(golden, append(b, '\n'), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	var w, g any
	_ = json.Unmarshal(want, &w)
	_ = json.Unmarshal(b, &g)
	if !reflect.DeepEqual(w, g) {
		t.Fatalf("golden mismatch\n got %s\nwant %s", b, want)
	}
}
