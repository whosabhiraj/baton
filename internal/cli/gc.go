package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/whosabhiraj/baton/internal/daemon"
)

type gcItem struct {
	RunID   string `json:"run_id"`
	AgentID string `json:"agent_id"`
	What    string `json:"what"`
	Reason  string `json:"reason"`
	Skipped string `json:"skipped"`
}

// callGC posts to /api/gc (admin token) and returns the report items.
func callGC(in *daemon.Info, token string, dry, force bool) ([]gcItem, error) {
	body, _ := json.Marshal(map[string]bool{"dry_run": dry, "force": force})
	req, _ := http.NewRequest("POST", strings.TrimRight(in.URL, "/")+"/api/gc", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Error string   `json:"error"`
		Items []gcItem `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("the running hub is older than this baton: restart it (`baton hub stop`, then `baton gc`)")
	}
	if resp.StatusCode != 200 {
		if r.Error == "" {
			r.Error = resp.Status
		}
		return nil, fmt.Errorf("gc: %s", r.Error)
	}
	return r.Items, nil
}

func gcLine(it gcItem) string {
	if it.Skipped != "" {
		return fmt.Sprintf("skip %s %s : %s", it.What, it.AgentID, it.Skipped)
	}
	return fmt.Sprintf("remove %s %s (%s)", it.What, it.AgentID, it.Reason)
}

// cmdGC implements `baton gc [--yes] [--force]`: dry run, list, ask, then the real run.
func (c *ctx) cmdGC(o opts) (int, error) {
	in, dataDir, err := c.ensureHub(o)
	if err != nil {
		return 0, err
	}
	token := readAdminToken(dataDir)
	force := o.flag("force")
	items, err := callGC(in, token, true, force)
	if err != nil {
		return 1, err
	}
	n := 0
	for _, it := range items {
		fmt.Fprintln(c.stdout, gcLine(it))
		if it.Skipped == "" {
			n++
		}
	}
	if n == 0 {
		fmt.Fprintln(c.stdout, "nothing to clean")
		return 0, nil
	}
	if !o.flag("yes") && !c.ask(fmt.Sprintf("Remove %d item(s)? [y/N] ", n)) {
		fmt.Fprintln(c.stderr, "baton: nothing removed")
		return 2, nil
	}
	items, err = callGC(in, token, false, force)
	if err != nil {
		return 1, err
	}
	done := 0
	for _, it := range items {
		if it.Skipped != "" {
			fmt.Fprintf(c.stdout, "skipped %s %s : %s\n", it.What, it.AgentID, it.Skipped)
			continue
		}
		done++
		fmt.Fprintf(c.stdout, "removed %s %s\n", it.What, it.AgentID)
	}
	fmt.Fprintf(c.stdout, "removed %d item(s)\n", done)
	return 0, nil
}
