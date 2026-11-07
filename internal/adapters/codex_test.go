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

func TestCodexArgv(t *testing.T) {
	pre := []string{"codex", "exec", "--json"}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	byp := []string{"--dangerously-bypass-approvals-and-sandbox"}
	dash := []string{"-"}
	cases := []struct {
		name          string
		acct          config.Account
		model, effort string
		want          []string
	}{
		{"empty is permissive", config.Account{}, "", "", cat(pre, byp, dash)},
		{"model default skipped", config.Account{}, "default", "", cat(pre, byp, dash)},
		{"model+effort", config.Account{}, "gpt-5", "medium", cat(pre, []string{"-m", "gpt-5", "-c", `model_reasoning_effort="medium"`}, byp, dash)},
		{"xhigh maps to high", config.Account{}, "", "xhigh", cat(pre, []string{"-c", `model_reasoning_effort="high"`}, byp, dash)},
		{"max maps to high", config.Account{}, "", "max", cat(pre, []string{"-c", `model_reasoning_effort="high"`}, byp, dash)},
		{"acceptEdits", config.Account{Permissions: config.Permissions{Mode: "acceptEdits"}}, "", "",
			cat(pre, []string{"--sandbox", "workspace-write", "--approve-for-me"}, dash)},
		{"default no flag + extra", config.Account{Permissions: config.Permissions{Mode: "default", ExtraArgs: []string{"--skip-git-repo-check"}}}, "", "",
			cat(pre, []string{"--skip-git-repo-check"}, dash)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := For("codex", config.Config{})
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
	for _, p := range []config.Permissions{{AllowedTools: []string{"x"}}, {DeniedTools: []string{"y"}}} {
		if _, err := (codex{}).Argv(config.Account{Permissions: p}, "", ""); err == nil || !strings.Contains(err.Error(), "claude-only") {
			t.Errorf("tools error: %v", err)
		}
	}
}

func TestCodexParserGolden(t *testing.T) {
	f, err := os.Open("../../protocol/fixtures/adapters/codex-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := codex{}.Parser()
	var got []goldenEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, e := range p.Parse(sc.Bytes()) {
			if _, perr := proto.ValidateHubPayload(e.Type, e.Payload); perr != nil {
				t.Fatalf("%s payload invalid: %v (%v)", e.Type, perr, e.Payload)
			}
			got = append(got, goldenEvent{e.Type, e.Payload})
		}
	}
	b, _ := json.MarshalIndent(got, "", "  ")
	const golden = "../../protocol/fixtures/adapters/codex-stream.golden.json"
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
	if p.Parse([]byte(`{"type":"item.started","item":null}`)) != nil || p.Parse([]byte(`[1]`)) != nil {
		t.Error("garbage emitted")
	}
}

func TestUnwrapShell(t *testing.T) {
	cases := map[string]string{
		`"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe" -Command 'baton hello; ls'`: "baton hello; ls",
		`"C:\Program Files\PowerShell\7\pwsh.exe" -NoProfile -Command "git status"`:              "git status",
		`/bin/bash -lc 'go test ./...'`: "go test ./...",
		`bash -lc ls`:                   "ls",
		`cmd /c dir`:                    "dir",
		`git status`:                    "git status",
		`python -c 'print(1)'`:          `python -c 'print(1)'`,
	}
	for in, want := range cases {
		if got := unwrapShell(in); got != want {
			t.Errorf("unwrapShell(%q) = %q, want %q", in, got, want)
		}
	}
}
