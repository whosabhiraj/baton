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

func TestOpencodeArgv(t *testing.T) {
	a, err := For("opencode", config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	run := []string{"opencode", "run", "--format", "json"}
	cases := []struct {
		name  string
		acct  config.Account
		model string
		want  []string
	}{
		{"permissive default", config.Account{}, "", append(append([]string{}, run...), "--auto")},
		{"model", config.Account{}, "openai/gpt-5", append(append([]string{}, run...), "-m", "openai/gpt-5", "--auto")},
		{"model default skipped", config.Account{}, "default", append(append([]string{}, run...), "--auto")},
		{"acceptEdits", config.Account{Permissions: config.Permissions{Mode: "acceptEdits"}}, "", run},
		{"default + extra", config.Account{Permissions: config.Permissions{Mode: "default", ExtraArgs: []string{"--pure"}}}, "", append(append([]string{}, run...), "--pure")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := a.Argv(c.acct, c.model, "high")
			if err != nil {
				t.Fatal(err)
			}
			eq(t, got, c.want)
		})
	}
	if _, err := a.Argv(config.Account{Permissions: config.Permissions{DeniedTools: []string{"x"}}}, "", ""); err == nil || !strings.Contains(err.Error(), "claude-only") {
		t.Errorf("tools error: %v", err)
	}
}

func TestOpencodeParserGolden(t *testing.T) {
	f, err := os.Open("../../protocol/fixtures/adapters/opencode-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := opencode{}.Parser()
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
	const golden = "../../protocol/fixtures/adapters/opencode-stream.golden.json"
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
	if p.Parse([]byte(`{"type":"tool_use","part":null}`)) != nil || p.Parse([]byte(`[1]`)) != nil {
		t.Error("garbage emitted")
	}
}
