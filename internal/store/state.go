package store

import (
	"encoding/json"
	"time"

	"github.com/whosabhiraj/baton/internal/proto"
)

// Step states reported in RunSummary.step_states.
const (
	StepPending = "pending"
	StepActive  = "active"
	StepDone    = "done"
	StepBlocked = "blocked"
)

// Question is an unanswered agent question (api.state open_question).
type Question struct {
	QID      string   `json:"qid"`
	Text     string   `json:"text"`
	Options  []string `json:"options,omitempty"`
	Blocking bool     `json:"blocking"`
	TS       string   `json:"ts,omitempty"`
	Seq      int64    `json:"seq,omitempty"`
}

// State is everything derived from a run's event log. It is rebuilt on startup by replaying
// events.jsonl through Apply, and kept current by calling Apply on every append.
type State struct {
	base      proto.RunStatus
	stalled   bool
	ended     bool
	LastSeq   int64
	LastTS    string
	Plan      []proto.PlanStep // effective plan (latest plan / plan_update), nil = none yet
	stepOrder []string
	Steps     map[string]string
	Questions []Question
	InboxN    int // highest to_agent n seen
	inboxSeen map[int]bool
}

func newState(recordPlan []proto.PlanStep) *State {
	s := &State{base: proto.StatusStarting, Steps: map[string]string{}, inboxSeen: map[int]bool{}}
	for _, st := range recordPlan {
		s.addStep(st.ID)
	}
	return s
}

func (s *State) addStep(id string) {
	if id == "" {
		return
	}
	if _, ok := s.Steps[id]; !ok {
		s.Steps[id] = StepPending
		s.stepOrder = append(s.stepOrder, id)
	}
}

// Status is the effective run status: the base status unless the run is stalled.
func (s *State) Status() proto.RunStatus {
	if s.stalled && !s.ended {
		return proto.StatusStalled
	}
	return s.base
}

// Ended reports whether a terminal event (done, killed, exit) was applied.
func (s *State) Ended() bool { return s.ended }

func (s *State) hasBlockingQuestion() bool {
	for _, q := range s.Questions {
		if q.Blocking {
			return true
		}
	}
	return false
}

// promote moves a run that was starting, blocked, rate limited or waiting (with nothing left
// to wait for) back to running. Paused stays paused until a resumed event.
func (s *State) promote() {
	switch s.base {
	case proto.StatusStarting, proto.StatusBlocked, proto.StatusRateLimited:
		s.base = proto.StatusRunning
	case proto.StatusWaiting:
		if !s.hasBlockingQuestion() {
			s.base = proto.StatusRunning
		}
	}
}

func parseSteps(v any) []proto.PlanStep {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []proto.PlanStep
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		title, _ := m["title"].(string)
		acc, _ := m["acceptance"].(string)
		if id != "" {
			out = append(out, proto.PlanStep{ID: id, Title: title, Acceptance: acc})
		}
	}
	return out
}

func num(v any) (int, bool) {
	switch x := v.(type) {
	case json.Number:
		n, err := x.Int64()
		return int(n), err == nil
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	}
	return 0, false
}

// Apply folds one stored event into the state. It is a pure reducer (no I/O, no clock).
func (s *State) Apply(ev proto.StoredEvent) {
	s.LastSeq = ev.Seq
	s.LastTS = ev.TS
	if ev.Src != proto.SrcHub {
		s.stalled = false
	}
	p := ev.Payload
	switch ev.Type {
	case "hello", "progress", "artifact", "error":
		s.promote()
	case "heartbeat":
		if s.base == proto.StatusStarting {
			s.base = proto.StatusRunning
		}
	case "plan":
		s.setPlan(parseSteps(p["steps"]))
		s.promote()
	case "step_start":
		if id, _ := p["step_id"].(string); id != "" {
			s.addStep(id)
			s.Steps[id] = StepActive
		}
		s.promote()
	case "step_done":
		if id, _ := p["step_id"].(string); id != "" {
			s.addStep(id)
			s.Steps[id] = StepDone
		}
		s.promote()
	case "step_blocked":
		if id, _ := p["step_id"].(string); id != "" {
			s.addStep(id)
			s.Steps[id] = StepBlocked
		}
		if !s.ended && s.base != proto.StatusPaused {
			s.base = proto.StatusBlocked
		}
	case "question":
		qid, _ := p["qid"].(string)
		text, _ := p["text"].(string)
		blocking, _ := p["blocking"].(bool)
		if qid != "" {
			s.removeQuestion(qid)
			q := Question{QID: qid, Text: text, Blocking: blocking, TS: ev.TS, Seq: ev.Seq}
			if arr, ok := p["options"].([]any); ok {
				for _, o := range arr {
					if str, ok := o.(string); ok {
						q.Options = append(q.Options, str)
					}
				}
			}
			s.Questions = append(s.Questions, q)
			if blocking && !s.ended && (s.base == proto.StatusRunning || s.base == proto.StatusStarting) {
				s.base = proto.StatusWaiting
			}
		}
	case "done":
		st, _ := p["status"].(string)
		if st == "failed" {
			s.base = proto.StatusFailed
		} else {
			s.base = proto.StatusDone
		}
		s.ended = true
	case "to_agent":
		if n, ok := num(p["n"]); ok {
			s.inboxSeen[n] = true
			if n > s.InboxN {
				s.InboxN = n
			}
		}
		typ, _ := p["type"].(string)
		inner, _ := p["payload"].(map[string]any)
		switch typ {
		case "answer":
			if qid, _ := inner["qid"].(string); qid != "" {
				s.removeQuestion(qid)
				s.promote()
			}
		case "plan_update":
			s.setPlan(parseSteps(inner["steps"]))
		}
	case "paused":
		if !s.ended {
			s.base = proto.StatusPaused
		}
	case "resumed":
		if !s.ended && s.base == proto.StatusPaused {
			s.base = proto.StatusRunning
			if s.hasBlockingQuestion() {
				s.base = proto.StatusWaiting
			}
		}
	case "rate_limited":
		if !s.ended {
			s.base = proto.StatusRateLimited
		}
	case "killed":
		if !s.ended {
			s.base = proto.StatusKilled
		}
		s.ended = true
	case "exit":
		if !s.ended {
			s.base = proto.StatusExited
		}
		s.ended = true
	case "stalled":
		s.stalled = true
	}
}

func (s *State) setPlan(steps []proto.PlanStep) {
	if len(steps) == 0 {
		return
	}
	s.Plan = steps
	for _, st := range steps {
		s.addStep(st.ID)
	}
}

func (s *State) removeQuestion(qid string) {
	out := s.Questions[:0]
	for _, q := range s.Questions {
		if q.QID != qid {
			out = append(out, q)
		}
	}
	s.Questions = out
}

// nowTS formats t the way event timestamps are written (RFC 3339, millisecond, UTC).
func nowTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
