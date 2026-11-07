package launch

import (
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/runs"
)

func TestPreflightKindMismatch(t *testing.T) {
	acct := &config.Account{ID: "c", Kind: "claude"}
	if err := preflight(acct, runs.SpawnResult{Kind: "opencode"}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
	if err := preflight(acct, runs.SpawnResult{Kind: "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := preflight(&config.Account{ID: "k", Kind: "mykind"}, runs.SpawnResult{Kind: "mykind"}); err != nil {
		t.Fatal(err)
	}
}
