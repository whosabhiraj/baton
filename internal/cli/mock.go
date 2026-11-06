package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/mock"
)

// runMock is a seam for tests.
var runMock = mock.Run

// cmdMock runs `baton mock`: a foreground hub with fake agents on a scratch repo.
func (c *ctx) cmdMock(o opts) (int, error) {
	opt := mock.Options{Speed: 1, Seed: 1, Once: o.flag("once"), Port: 7777, Out: c.stdout}
	if o.has("speed") {
		f, err := strconv.ParseFloat(strings.TrimSpace(o.get("speed")), 64)
		if err != nil || f <= 0 || f > 1000 {
			return 0, usagef("--speed must be a number above 0 (up to 1000), got %q", o.get("speed"))
		}
		opt.Speed = f
	}
	if o.has("seed") {
		n, err := strconv.ParseInt(strings.TrimSpace(o.get("seed")), 10, 64)
		if err != nil {
			return 0, usagef("--seed must be a whole number, got %q", o.get("seed"))
		}
		opt.Seed = n
	}
	if o.flag("reset") {
		if err := mock.Reset(); err != nil {
			return 0, err
		}
	}
	stop, cancel := upStopCtx()
	defer cancel()
	err := runMock(stop, opt, func(h *hub.Hub, _ *mock.Engine) {
		fmt.Fprintf(c.stdout, "baton mock: %s\n", h.URL)
		fmt.Fprintf(c.stdout, "admin token: %s\n", h.AdminToken)
		fmt.Fprintf(c.stdout, "data dir: %s (fake agents only; Ctrl+C stops)\n", mock.Dir())
		if !o.flag("no-browser") && config.Load(mock.ConfigPath(), "").Config.Hub.OpenBrowser {
			openBrowser(dashboardURL(h.URL, h.AdminToken))
		}
	})
	return 0, err
}
