package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
)

// agent is one fake agent. It talks to the hub like the real `baton` CLI does: POST /api/ingest
// with its run token and GET /api/runs/{id}/inbox. Traits pick what happens to it along the way:
// ask, block, limit, stall, pause, kill, crash, fail (see the script in engine.go).
type agent struct {
	e       *Engine
	res     runs.SpawnResult
	traits  map[string]bool
	rng     *rand.Rand
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	started time.Time

	steps   []proto.PlanStep
	after   int             // last inbox n seen
	answers map[string]bool // answered qids
	gotMsg  bool
	paused  bool
	stop    bool
	pct     int
	use     map[string]float64 // cumulative usage
}

var tools = map[string][]string{
	"edit":   {"internal/api/handlers.go (+24 -3)", "src/components/List.tsx (+41 -9)", "internal/store/cache.go (+12 -0)", "README.md (+6 -1)"},
	"shell":  {"go build ./...", "git status --short", "npm run lint", "git diff --stat"},
	"read":   {"docs/DESIGN.md", "internal/config/load.go", "src/index.ts", "go.mod"},
	"test":   {"go test ./... (42 passed)", "go test ./internal/api -run TestList (ok)", "npm test (18 passed, 1 skipped)"},
	"search": {"rg \"TODO\" internal/", "rg -n \"func New\" .", "grep -r \"pagination\" src/"},
}
var toolNames = []string{"edit", "edit", "shell", "read", "read", "test", "search"}

var stepPool = [][2]string{
	{"Read the existing code", "Relevant modules and call sites identified"},
	{"Sketch the change", "Approach written down and consistent with the design doc"},
	{"Implement the change", "Code compiles and the new behaviour works locally"},
	{"Cover it with tests", "New tests pass and fail without the change"},
	{"Clean up and document", "Lint is clean and the README mentions the change"},
}

func (a *agent) alive() bool { return a.ctx.Err() == nil && !a.stop }

// sleep waits sec (scaled by speed); false if the agent was cancelled.
func (a *agent) sleep(sec float64) bool {
	select {
	case <-a.ctx.Done():
		return false
	case <-time.After(a.e.d(sec)):
		return true
	}
}

func (a *agent) run() {
	defer close(a.done)
	a.send("hello", map[string]any{"brief_ack": true, "model": a.res.Model, "cwd": a.res.Worktree})
	if r := a.e.hub.Store().Get(a.res.RunID); r != nil { // keep the plan given at spawn
		a.steps = append(a.steps, r.Record().Plan...)
	}
	if n := 3 + a.rng.Intn(3); len(a.steps) == 0 { // rng drawn either way: seeded runs stay reproducible
		for i := 0; i < n; i++ {
			s := stepPool[i]
			a.steps = append(a.steps, proto.PlanStep{ID: fmt.Sprintf("s%d", i+1), Title: s[0], Acceptance: s[1]})
		}
	}
	a.send("plan", map[string]any{"steps": stepsJSON(a.steps)})
	for i := 0; i < len(a.steps) && a.alive(); i++ {
		st := a.steps[i]
		a.send("step_start", map[string]any{"step_id": st.ID, "note": st.Title})
		ticks := 4 + a.rng.Intn(3)
		for k := 0; k < ticks && a.alive(); k++ {
			a.pct = min(99, (i*ticks+k+1)*100/(len(a.steps)*ticks))
			a.work()
			if i == 1 && k == 1 {
				a.trait()
			}
			if i == 2 && k == 1 && a.traits["crash"] {
				a.e.appendHub(a.res.RunID, "exit", map[string]any{"code": 1, "signal": nil, "diff_stat": nil,
					"duration_s": time.Since(a.started).Seconds(), "detail": "agent process crashed"})
				return
			}
		}
		if !a.alive() {
			break
		}
		a.send("step_done", map[string]any{"step_id": st.ID, "summary": st.Title + " - done"})
	}
	if a.ctx.Err() != nil {
		return // killed: the launcher appends the end events
	}
	status, summary, code := "success", "All steps done; acceptance checks pass.", 0
	switch {
	case a.stop:
		status, summary = "partial", "Stopped on request after part of the plan."
	case a.traits["fail"]:
		status, summary, code = "failed", "Could not get the tests to pass; see the last test run.", 1
	}
	a.e.setOutcome(a.res.RunID, status)
	a.send("done", map[string]any{"status": status, "summary": summary})
	a.e.appendHub(a.res.RunID, "exit", map[string]any{"code": code, "signal": nil, "duration_s": time.Since(a.started).Seconds(),
		"diff_stat": fmt.Sprintf(" %d files changed, %d insertions(+), %d deletions(-)", 2+a.rng.Intn(6), 20+a.rng.Intn(200), a.rng.Intn(40))})
}

func stepsJSON(ss []proto.PlanStep) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = map[string]any{"id": s.ID, "title": s.Title, "acceptance": s.Acceptance}
	}
	return out
}

// trait is where the special behaviours kick in (during step 2).
func (a *agent) trait() {
	id := a.steps[min(1, len(a.steps)-1)].ID
	switch {
	case a.traits["ask"]:
		qid := ids.QID()
		a.send("question", map[string]any{"qid": qid, "blocking": true, "text": "Should the new endpoint stay backwards compatible with v1 clients?",
			"options": []any{"yes, keep v1", "no, break it"}})
		for !a.answers[qid] && a.alive() {
			a.idle(3)
		}
	case a.traits["block"]:
		a.gotMsg = false
		a.send("step_blocked", map[string]any{"step_id": id, "reason": "Needs the schema change from another agent to land first"})
		a.waitMsg(25)
	case a.traits["limit"]:
		a.gotMsg = false
		a.e.appendHub(a.res.RunID, "rate_limited", map[string]any{"detail": "429 from provider, retry later"})
		a.waitMsg(25)
		a.send("progress", map[string]any{"percent": a.pct, "note": "rate limit cleared, continuing"})
	case a.traits["stall"]:
		a.sleep(a.e.stall.Seconds() * a.e.speed * 1.6) // silent: no heartbeat, no inbox
	}
}

// waitMsg idles until a message arrives or sec (scaled) have passed.
func (a *agent) waitMsg(sec float64) {
	for t := 0.0; !a.gotMsg && t < sec && a.alive(); t += 3 {
		a.idle(3)
	}
}

// idle waits sec, keeps the heartbeat going, reads the inbox, and holds while paused.
func (a *agent) idle(sec float64) {
	a.sleep(sec)
	a.send("heartbeat", map[string]any{})
	a.poll()
	for a.paused && a.alive() {
		a.sleep(1.5)
		a.send("heartbeat", map[string]any{})
		a.poll()
	}
}

// work is one tick of normal work: activity, progress, now and then a commit and usage.
func (a *agent) work() {
	a.idle(4 + a.rng.Float64()*2)
	if !a.alive() {
		return
	}
	tool := toolNames[a.rng.Intn(len(toolNames))]
	d := tools[tool][a.rng.Intn(len(tools[tool]))]
	a.e.appendHub(a.res.RunID, "activity", map[string]any{"tool": tool, "detail": d})
	a.send("progress", map[string]any{"percent": a.pct, "note": a.steps[min(a.pct*len(a.steps)/100, len(a.steps)-1)].Title})
	if a.rng.Intn(4) == 0 {
		a.send("artifact", map[string]any{"kind": "commit", "ref": fmt.Sprintf("%07x", a.rng.Uint32()&0xfffffff), "note": "wip"})
	}
	a.addUsage()
}

func (a *agent) addUsage() {
	in, out := float64(800+a.rng.Intn(1700)), float64(200+a.rng.Intn(700))
	a.use["input_tokens"] += in
	a.use["output_tokens"] += out
	p := map[string]any{"input_tokens": int64(a.use["input_tokens"]), "output_tokens": int64(a.use["output_tokens"]), "model": a.res.Model}
	if a.res.Kind == "claude" {
		a.use["cache_read_tokens"] += float64(3000 + a.rng.Intn(6000))
		a.use["cache_write_tokens"] += float64(a.rng.Intn(500))
		a.use["cost_usd"] += in*3e-6 + out*15e-6
		p["cache_read_tokens"] = int64(a.use["cache_read_tokens"])
		p["cache_write_tokens"] = int64(a.use["cache_write_tokens"])
		p["cost_usd"] = float64(int(a.use["cost_usd"]*10000)) / 10000
	}
	a.e.appendHub(a.res.RunID, "usage", p)
}

// poll reads new inbox items and reacts to them.
func (a *agent) poll() {
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/runs/%s/inbox?after=%d", a.res.Env["BATON_URL"], a.res.RunID, a.after), nil)
	req.Header.Set("Authorization", "Bearer "+a.res.Env["BATON_TOKEN"])
	resp, err := a.e.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var body struct {
		Messages []struct {
			N       int            `json:"n"`
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		} `json:"messages"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return
	}
	for _, m := range body.Messages {
		a.after = m.N
		switch m.Type {
		case "answer":
			qid, _ := m.Payload["qid"].(string)
			a.answers[qid] = true
		case "message":
			a.gotMsg = true
			text, _ := m.Payload["text"].(string)
			a.send("progress", map[string]any{"percent": a.pct, "note": "Noted message: " + trunc(text, 60)})
		case "pause":
			a.e.appendHub(a.res.RunID, "paused", map[string]any{"reason": "pause requested"})
			a.paused = true
		case "resume":
			if a.paused {
				a.e.appendHub(a.res.RunID, "resumed", map[string]any{})
				a.paused = false
			}
		case "stop":
			a.stop = true
		case "plan_update":
			if ss, ok := m.Payload["steps"].([]any); ok && len(ss) > 0 {
				a.steps = a.steps[:0]
				for _, s := range ss {
					sm, _ := s.(map[string]any)
					id, _ := sm["id"].(string)
					title, _ := sm["title"].(string)
					acc, _ := sm["acceptance"].(string)
					a.steps = append(a.steps, proto.PlanStep{ID: id, Title: title, Acceptance: acc})
				}
			}
		}
	}
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// send posts one envelope to /api/ingest, as the baton CLI does.
func (a *agent) send(typ string, p map[string]any) {
	b, _ := json.Marshal(map[string]any{"v": 1, "run_id": a.res.RunID, "agent_id": a.res.AgentID,
		"ts": proto.NowTS(), "type": typ, "payload": p, "msg_id": ids.UUID()})
	req, _ := http.NewRequest("POST", a.res.Env["BATON_URL"]+"/api/ingest", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+a.res.Env["BATON_TOKEN"])
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.e.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// 410/401 after the run ended (killed, stopped) are expected, anything else is a bug.
	if resp.StatusCode >= 300 && resp.StatusCode != 410 && resp.StatusCode != 401 {
		a.e.fail(fmt.Sprintf("ingest %s -> %d %s", typ, resp.StatusCode, strings.TrimSpace(string(raw))))
	}
}
