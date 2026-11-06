package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/hub"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
)

// scripted is the opening scenario: six agents on the three accounts that between them reach
// every run status the dashboard shows.
var scripted = []struct {
	account, role, model, task string
	traits                     []string
}{
	{"claude-a", "planner", "opus", "Draft the plan for the API pagination work", []string{"ask"}},
	{"claude-b", "implementer", "sonnet", "Add cursor pagination to the list endpoint", []string{"block"}},
	{"claude-b", "tester", "sonnet", "Write regression tests for the auth middleware", []string{"pause", "fail"}},
	{"codex-1", "implementer", "default", "Implement a token-bucket rate limiter", []string{"limit"}},
	{"codex-1", "refactorer", "default", "Refactor the config loader into smaller functions", []string{"stall", "kill"}},
	{"claude-a", "reviewer", "opus", "Review the payments change before release", []string{"crash"}},
}

// random agents (after the script) get 0-2 of these.
var traitPool = []string{"ask", "block", "limit", "stall", "pause", "fail", "crash", "", "", "", ""}

var randomTasks = []string{
	"Fix flaky test in the scheduler", "Add input validation to the signup form", "Speed up the search index build",
	"Document the plugin API", "Remove the deprecated v1 handlers", "Add retry with backoff to the uploader",
}

// Engine is the fake conductor plus the fake agents. It is the hub's Launcher and OnApproval hook.
type Engine struct {
	speed  float64
	seed   int64
	once   bool
	out    io.Writer
	stall  time.Duration // hub heartbeat timeout
	client *http.Client

	hub *hub.Hub
	ctx context.Context
	wg  sync.WaitGroup

	mu       sync.Mutex
	agents   map[string]*agent
	pending  []string          // traits for the next Start (set by the conductor around a spawn)
	outcome  map[string]string // run id -> done status
	errs     []string          // protocol errors seen by fake agents (tests assert none)
	nStarted int
}

func newEngine(o Options, stall time.Duration) *Engine {
	return &Engine{speed: o.Speed, seed: o.Seed, once: o.Once, out: o.Out, stall: stall,
		client: &http.Client{Timeout: 10 * time.Second}, agents: map[string]*agent{}, outcome: map[string]string{}}
}

// d turns scenario seconds into real time.
func (e *Engine) d(sec float64) time.Duration {
	return time.Duration(sec / e.speed * float64(time.Second))
}

func (e *Engine) logf(f string, a ...any) {
	if e.out != nil {
		fmt.Fprintf(e.out, "mock: "+f+"\n", a...)
	}
}

func (e *Engine) fail(msg string) {
	e.mu.Lock()
	e.errs = append(e.errs, msg)
	e.mu.Unlock()
	e.logf("ERROR %s", msg)
}

// Errors returns the protocol errors fake agents ran into.
func (e *Engine) Errors() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.errs...)
}

func (e *Engine) setOutcome(runID, status string) {
	e.mu.Lock()
	e.outcome[runID] = status
	e.mu.Unlock()
}

// appendHub adds an adapter-type event (what a real adapter would observe) to a run.
func (e *Engine) appendHub(runID, typ string, p map[string]any) {
	if r := e.hub.Store().Get(runID); r != nil {
		if _, perr := r.AppendHub(typ, p); perr != nil {
			e.fail(fmt.Sprintf("append %s: %v", typ, perr))
		}
	}
}

// Start implements runs.Launcher: it starts a fake agent for a fresh run.
func (e *Engine) Start(res runs.SpawnResult) error {
	if e.hub == nil || e.ctx == nil || e.ctx.Err() != nil {
		return errors.New("mock engine is not running")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nStarted++
	ctx, cancel := context.WithCancel(e.ctx)
	a := &agent{e: e, res: res, traits: map[string]bool{}, rng: rand.New(rand.NewSource(e.seed*1000 + int64(e.nStarted))),
		ctx: ctx, cancel: cancel, done: make(chan struct{}), started: time.Now(), answers: map[string]bool{},
		use: map[string]float64{}}
	for _, t := range e.pending {
		a.traits[t] = true
	}
	e.pending = nil
	e.agents[res.RunID] = a
	e.wg.Add(1)
	go func() { defer e.wg.Done(); a.run() }()
	return nil
}

// Kill implements runs.Launcher: stop the fake agent, then append killed and exit.
func (e *Engine) Kill(runID string) error {
	e.mu.Lock()
	a := e.agents[runID]
	e.mu.Unlock()
	if a == nil {
		return errors.New("no such agent")
	}
	a.cancel()
	select {
	case <-a.done:
	case <-time.After(5 * time.Second):
	}
	e.appendHub(runID, "killed", map[string]any{"reason": "killed on request"})
	e.appendHub(runID, "exit", map[string]any{"code": -1, "signal": "SIGKILL", "diff_stat": nil, "duration_s": time.Since(a.started).Seconds()})
	return nil
}

// OnApproval reacts to a human decision: a merge marks the run merged, a rejection tells the agent.
func (e *Engine) OnApproval(ap approvals.Approval) {
	r := e.hub.Store().Get(ap.RunID)
	if r == nil {
		return
	}
	switch {
	case ap.Kind == approvals.KindMerge && ap.Status == approvals.Approved:
		_ = r.SetExtra("merged", true)
		e.logf("merged %s", ap.RunID)
	case ap.Status == approvals.Rejected:
		if !r.Ended() {
			_, _ = r.Deliver("message", map[string]any{"from": "conductor", "text": "The request was rejected: " + ap.Note})
		}
		e.logf("approval %s rejected", ap.ID)
	}
}

// ---- conductor ----

type runView struct {
	RunID         string `json:"run_id"`
	AgentID       string `json:"agent_id"`
	Callsign      string `json:"callsign"`
	Status        string `json:"status"`
	OpenQuestions []struct {
		QID string `json:"qid"`
	} `json:"open_questions"`
	StepStates map[string]string `json:"step_states"`
}

func isEnded(status string) bool {
	switch status {
	case "done", "failed", "killed", "exited":
		return true
	}
	return false
}

// api calls the hub as the conductor, through the real HTTP API.
func (e *Engine) api(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.hub.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+e.hub.AdminToken)
	req.Header.Set("X-Baton-Actor", "conductor")
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

func (e *Engine) setConductor(state string) {
	e.hub.SetConductor(proto.ConductorState{State: state, Account: "claude-a", Model: "opus"})
}

// pause is a scenario sleep; false if the engine is stopping.
func (e *Engine) pause(sec float64) bool {
	select {
	case <-e.ctx.Done():
		return false
	case <-time.After(e.d(sec)):
		return true
	}
}

// spawn asks the hub for an agent with the given traits.
func (e *Engine) spawn(account, role, model, task string, traits []string) string {
	e.mu.Lock()
	e.pending = traits
	e.mu.Unlock()
	var res runs.SpawnResult
	st, err := e.api("POST", "/api/spawn", map[string]any{"account": account, "role": role, "model": model, "task": task}, &res)
	e.mu.Lock()
	e.pending = nil
	e.mu.Unlock()
	if err != nil || st != 200 {
		e.logf("spawn on %s refused (%d)", account, st)
		return ""
	}
	e.logf("spawned %s on %s as %s", res.Callsign, account, role)
	return res.RunID
}

// proposeLineup submits a two-agent lineup like a real conductor would (propose mode: it waits
// for the user; approving it spawns fake agents through the normal launcher).
func (e *Engine) proposeLineup() {
	step := func(id, title, acc string) map[string]any {
		return map[string]any{"id": id, "title": title, "acceptance": acc}
	}
	body := map[string]any{
		"goal": "Add rate limiting to the public API",
		"note": "Middleware and tests split so they can run in parallel; sonnet for the code, codex for the tests.",
		"agents": []any{
			map[string]any{"account": "claude-b", "role": "implementer", "model": "sonnet", "effort": "medium",
				"task": "Add a token-bucket rate limiter middleware to the public API routes.",
				"plan": []any{step("s1", "Write the limiter", "unit tests for the bucket pass"),
					step("s2", "Wire it into the router", "GET /api/items returns 429 after the limit")}},
			map[string]any{"account": "codex-1", "role": "implementer", "model": "default", "effort": "low",
				"task": "Write integration tests for rate limiting.",
				"plan": []any{step("s1", "Cover the 429 path", "go test ./api/... passes")}},
		},
	}
	var out map[string]any
	st, err := e.api("POST", "/api/lineups", body, &out)
	if err != nil {
		return
	}
	e.logf("conductor: proposed a lineup (%d %v)", st, out["status"])
}

// conduct runs the fake conductor until ctx ends: the opening script, then (unless once) a steady
// stream of fresh agents. It also answers, nudges, pauses, resumes, kills and asks for merges.
func (e *Engine) conduct(ctx context.Context) {
	rng := rand.New(rand.NewSource(e.seed))
	first := map[string]time.Time{} // run|status -> first seen
	pausedByMe, pausedOnce, killSent, merged := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	var script []string // run ids of the opening script
	begin := time.Now()
	nScript := 0
	e.setConductor("idle")
	go func() { // one lineup for the user to edit and approve (A18)
		if e.pause(4) {
			e.proposeLineup()
		}
	}()
	tick := time.NewTicker(max(e.d(2), 20*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.hub.Done():
			return
		case <-tick.C:
		}
		var st struct {
			Runs []runView `json:"runs"`
		}
		if code, err := e.api("GET", "/api/state", nil, &st); err != nil || code != 200 {
			continue
		}
		e.setConductor("thinking")
		if !e.pause(0.7) {
			return
		}
		acted, live := false, 0
		act := func(f string, a ...any) {
			if !acted {
				acted = true
				e.setConductor("acting")
			}
			e.logf("conductor: "+f, a...)
		}
		for _, r := range st.Runs {
			if isEnded(r.Status) {
				if r.Status == "done" && !merged[r.RunID] {
					merged[r.RunID] = true
					e.mu.Lock()
					ok := e.outcome[r.RunID] == "success"
					e.mu.Unlock()
					if ok {
						act("requesting merge approval for %s", r.RunID)
						base := "main"
						if run := e.hub.Store().Get(r.RunID); run != nil && run.Record().BaseBranch != "" {
							base = run.Record().BaseBranch
						}
						_, _ = e.hub.RequestApproval(approvals.KindMerge, r.RunID, "Merge "+r.Callsign+" (baton/"+r.AgentID+") into "+base,
							map[string]any{"branch": "baton/" + r.AgentID, "base_branch": base, "summary": "Acceptance checks pass"}, "conductor")
					}
				}
				continue
			}
			live++
			key := r.RunID + "|" + r.Status
			if _, ok := first[key]; !ok {
				first[key] = time.Now()
			}
			since := time.Since(first[key])
			e.mu.Lock()
			a := e.agents[r.RunID]
			e.mu.Unlock()
			post := func(what, path string, body any) {
				act("%s %s", what, r.RunID)
				_, _ = e.api("POST", "/api/runs/"+r.RunID+"/"+path, body, nil)
			}
			switch {
			case r.Status == "waiting" && since > e.d(30):
				for _, q := range r.OpenQuestions {
					post("answering", "answer", map[string]any{"qid": q.QID, "text": "Yes, keep v1 working; deprecate it later."})
				}
			case r.Status == "blocked" && since > e.d(15), r.Status == "rate_limited" && since > e.d(12):
				first[key] = time.Now()
				post("nudging", "message", map[string]any{"from": "conductor", "text": "The dependency is ready, carry on."})
			case r.Status == "paused" && pausedByMe[r.RunID] && since > e.d(15):
				pausedByMe[r.RunID] = false
				post("resuming", "resume", map[string]any{})
			case a != nil && a.traits["pause"] && !pausedOnce[r.RunID] && r.Status == "running" && doneSteps(r) >= 1:
				pausedOnce[r.RunID] = true
				pausedByMe[r.RunID] = true
				post("pausing", "pause", map[string]any{})
			case a != nil && a.traits["kill"] && !killSent[r.RunID] && time.Since(a.started) > e.d(60):
				killSent[r.RunID] = true
				post("killing", "kill", nil)
			case r.Status == "running" && rng.Intn(40) == 0:
				post("messaging", "message", map[string]any{"from": "conductor", "text": "Keep commits small please."})
			}
		}
		// the opening script: one agent every few seconds
		for nScript < len(scripted) && time.Since(begin) >= e.d(2*float64(nScript)) {
			s := scripted[nScript]
			nScript++
			act("spawning %s", s.role)
			if id := e.spawn(s.account, s.role, s.model, s.task, s.traits); id != "" {
				script = append(script, id)
			}
			live++
		}
		if !e.once && nScript == len(scripted) && live < 6 && allEnded(st.Runs, script) {
			t := []string{traitPool[rng.Intn(len(traitPool))], traitPool[rng.Intn(len(traitPool))]}
			acc := []struct{ id, role, model string }{{"claude-a", "planner", "opus"}, {"claude-b", "implementer", "sonnet"},
				{"claude-b", "tester", "sonnet"}, {"codex-1", "implementer", "default"}, {"codex-1", "refactorer", "default"}}[rng.Intn(5)]
			act("spawning a fresh %s", acc.role)
			e.spawn(acc.id, acc.role, acc.model, randomTasks[rng.Intn(len(randomTasks))], t)
		}
		if acted && !e.pause(0.7) {
			return
		}
		e.setConductor("idle")
	}
}

func doneSteps(r runView) int {
	n := 0
	for _, s := range r.StepStates {
		if s == "done" {
			n++
		}
	}
	return n
}

// allEnded: every run in ids is over.
func allEnded(rs []runView, ids []string) bool {
	over := map[string]bool{}
	for _, r := range rs {
		over[r.RunID] = isEnded(r.Status)
	}
	for _, id := range ids {
		if !over[id] {
			return false
		}
	}
	return true
}

// stopAgents cancels every fake agent and waits for them.
func (e *Engine) stopAgents() {
	e.mu.Lock()
	for _, a := range e.agents {
		a.cancel()
	}
	e.mu.Unlock()
	e.wg.Wait()
}
