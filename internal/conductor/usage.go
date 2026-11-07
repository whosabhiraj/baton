package conductor

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/whosabhiraj/baton/internal/store"
)

// UsageFile keeps the conductor's token and cost totals per session.
const UsageFile = "usage.json"

// SessionUsage is one conversation's totals. claude's `result` line carries running totals for
// the whole session (total_cost_usd, modelUsage), and they carry over a --resume, so the latest
// result of a session is its total.
type SessionUsage struct {
	Account string  `json:"account"`
	CostUSD float64 `json:"cost_usd"`
	In      int64   `json:"input_tokens"`
	Out     int64   `json:"output_tokens"`
	CacheR  int64   `json:"cache_read_tokens"`
	CacheW  int64   `json:"cache_write_tokens"`
}

func (m *Manager) loadUsage() {
	m.usage = map[string]SessionUsage{}
	if b, err := os.ReadFile(filepath.Join(m.dir, UsageFile)); err == nil {
		_ = json.Unmarshal(b, &m.usage)
	}
}

// noteResultLocked records a `result` line's session totals.
func (m *Manager) noteResultLocked(line []byte) {
	var r struct {
		SessionID    string   `json:"session_id"`
		TotalCostUSD *float64 `json:"total_cost_usd"`
		ModelUsage   map[string]struct {
			In     int64 `json:"inputTokens"`
			Out    int64 `json:"outputTokens"`
			CacheR int64 `json:"cacheReadInputTokens"`
			CacheW int64 `json:"cacheCreationInputTokens"`
		} `json:"modelUsage"`
	}
	if json.Unmarshal(line, &r) != nil {
		return
	}
	sid := r.SessionID
	if sid == "" {
		sid = m.sess.SessionID
	}
	if sid == "" || (r.TotalCostUSD == nil && len(r.ModelUsage) == 0) {
		return
	}
	u := SessionUsage{Account: m.cur.acct.ID}
	if r.TotalCostUSD != nil && *r.TotalCostUSD >= 0 {
		u.CostUSD = *r.TotalCostUSD
	}
	for _, mu := range r.ModelUsage {
		u.In += mu.In
		u.Out += mu.Out
		u.CacheR += mu.CacheR
		u.CacheW += mu.CacheW
	}
	if old, ok := m.usage[sid]; ok && old.Account != "" {
		u.Account = old.Account // a session belongs to the account it started on
	}
	m.usage[sid] = u
	b, _ := json.MarshalIndent(m.usage, "", "  ")
	if err := store.WriteFileAtomic(filepath.Join(m.dir, UsageFile), b, 0o600); err != nil {
		m.log.Warn("conductor usage write failed", "err", err)
	}
}

// Usage returns the conductor's totals per account, in the shape of run usage
// (input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd).
func (m *Manager) Usage() map[string]map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]map[string]any{}
	for _, u := range m.usage {
		if u.Account == "" {
			continue
		}
		t := out[u.Account]
		if t == nil {
			t = map[string]any{"input_tokens": 0.0, "output_tokens": 0.0, "cache_read_tokens": 0.0, "cache_write_tokens": 0.0, "cost_usd": 0.0}
			out[u.Account] = t
		}
		t["input_tokens"] = t["input_tokens"].(float64) + float64(u.In)
		t["output_tokens"] = t["output_tokens"].(float64) + float64(u.Out)
		t["cache_read_tokens"] = t["cache_read_tokens"].(float64) + float64(u.CacheR)
		t["cache_write_tokens"] = t["cache_write_tokens"].(float64) + float64(u.CacheW)
		t["cost_usd"] = t["cost_usd"].(float64) + u.CostUSD
	}
	return out
}
