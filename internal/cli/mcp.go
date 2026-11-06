package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/whosabhiraj/baton/internal/daemon"
	"github.com/whosabhiraj/baton/internal/mcpserver"
)

// cmdMCP is `baton mcp [--project P] [--url U]`: the conductor's MCP stdio server (A16). The
// hub starts it through the conductor CLI's MCP config with BATON_CONDUCTOR_TOKEN set. Stdout
// carries MCP frames only.
func (c *ctx) cmdMCP(o opts) (int, error) {
	tok := c.getenv("BATON_CONDUCTOR_TOKEN")
	if tok == "" {
		return 0, errors.New("BATON_CONDUCTOR_TOKEN is not set (the hub sets it when it starts the conductor)")
	}
	base := func() (string, error) { return o.get("url"), nil }
	if !o.has("url") {
		_, dataDir, err := project(o)
		if err != nil {
			return 0, err
		}
		base = func() (string, error) {
			in, err := daemon.ReadInfo(dataDir)
			if err != nil {
				return "", errors.New("no hub running for this project (start it with `baton up`)")
			}
			return in.URL, nil
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := mcpserver.Serve(ctx, &mcpserver.Client{BaseURL: base, Token: tok}, Version, c.stdin, c.stdout)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		return 1, err
	}
	return 0, nil
}
