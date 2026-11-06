package cli

import "github.com/whosabhiraj/baton/internal/supervise"

// cmdSupervise is the hidden `__supervise` command: the per-agent supervisor (not in help).
func (c *ctx) cmdSupervise() (int, error) { return supervise.Run(c.stdin), nil }
