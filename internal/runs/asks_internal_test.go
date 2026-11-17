package runs

import (
	"testing"

	"github.com/whosabhiraj/baton/internal/config"
)

func TestAsksUser(t *testing.T) {
	for _, c := range []struct {
		kind, mode string
		ask, want  bool
	}{
		{"claude", config.ModeSandboxed, true, true},
		{"codex", config.ModeSandboxed, true, true},
		{"claude", "acceptEdits", true, true},
		{"claude", "default", true, true},
		{"claude", "permissive", true, false}, // nothing prompts
		{"codex", "acceptEdits", true, false}, // codex only asks through its sandbox driver
		{"mine", config.ModeSandboxed, true, false},
		{"claude", config.ModeSandboxed, false, false},
	} {
		if got := asksUser(c.kind, config.Permissions{Mode: c.mode, Ask: c.ask}); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}
