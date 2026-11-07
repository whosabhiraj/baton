package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/redact"
)

type idxEntry struct {
	seq int64
	off int64
}

// Run is one agent run: its record, event log, inbox and derived state. All methods are safe
// for concurrent use (one mutex per run).
type Run struct {
	s       *Store
	id      string
	agentID string
	created string
	dir     string

	mu        sync.Mutex
	rec       proto.RunRecord
	savedStat proto.RunStatus
	st        *State
	seen      map[string]int64
	idx       []idxEntry
	f         *os.File
	size      int64
	lastAct   time.Time
	nextInbox int
	closed    bool
}

// Summary is the RunSummary of GET /api/state: the run record without the token hash plus
// derived fields.
type Summary struct {
	RunID         string            `json:"run_id"`
	AgentID       string            `json:"agent_id"`
	Callsign      string            `json:"callsign,omitempty"`
	AccountID     string            `json:"account_id,omitempty"`
	Role          string            `json:"role,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	Model         string            `json:"model,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	BaseBranch    string            `json:"base_branch,omitempty"`
	Status        proto.RunStatus   `json:"status"`
	CreatedAt     string            `json:"created_at"`
	EndedAt       string            `json:"ended_at,omitempty"`
	TokenRevoked  bool              `json:"token_revoked"`
	Plan          []proto.PlanStep  `json:"plan"`
	LastSeq       int64             `json:"last_seq"`
	LastEventTS   string            `json:"last_event_ts,omitempty"`
	OpenQuestions []Question        `json:"open_questions"`
	StepStates    map[string]string `json:"step_states"`
	Usage         map[string]any    `json:"usage,omitempty"`  // latest cumulative usage (A15)
	Merged        bool              `json:"merged,omitempty"` // branch merged into its base (A16; run.json extra "merged")
}

// loadRun opens an existing run directory: reads run.json, recovers events.jsonl, rebuilds
// state and repairs inbox files that have no to_agent event.
func (s *Store) loadRun(dir string) (*Run, error) {
	b, err := os.ReadFile(filepath.Join(dir, paths.RunRecordFile))
	if err != nil {
		return nil, err
	}
	var rec proto.RunRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, err
	}
	if rec.RunID == "" {
		return nil, errors.New("run.json has no run_id")
	}
	r := &Run{
		s: s, id: rec.RunID, agentID: rec.AgentID, created: rec.CreatedAt, dir: dir, rec: rec,
		savedStat: rec.Status, st: newState(rec.Plan), seen: map[string]int64{}, lastAct: time.Now(),
	}
	if err := r.recoverEvents(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, paths.EventsFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	r.f = f
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repairInboxLocked()
	if r.st.ended && !r.rec.TokenRevoked {
		r.rec.TokenRevoked = true
		if r.rec.EndedAt == "" {
			r.rec.EndedAt = r.st.LastTS
		}
	}
	r.saveRecordLocked()
	return r, nil
}

// recoverEvents scans events.jsonl, truncating a torn or unparsable final line, and rebuilds
// seq, msg_id set, offset index and derived state.
func (r *Run) recoverEvents() error {
	path := filepath.Join(r.dir, paths.EventsFile)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	br := bufio.NewReaderSize(f, 64*1024)
	var off int64
	truncateAt := int64(-1)
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) == 0 && rerr != nil {
			break
		}
		start := off
		off += int64(len(line))
		if rerr != nil { // no trailing newline: torn write
			truncateAt = start
			break
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var ev proto.StoredEvent
		if err := json.Unmarshal(trimmed, &ev); err != nil || ev.Seq <= 0 {
			if _, perr := br.Peek(1); perr == io.EOF {
				truncateAt = start
				break
			}
			r.s.log.Warn("skipping corrupt event line", "run", r.id, "offset", start)
			continue
		}
		r.idx = append(r.idx, idxEntry{ev.Seq, start})
		r.seen[ev.MsgID] = ev.Seq
		r.st.Apply(ev)
	}
	f.Close()
	if truncateAt >= 0 {
		r.s.log.Warn("truncating torn last line of events.jsonl", "run", r.id, "offset", truncateAt)
		if err := os.Truncate(path, truncateAt); err != nil {
			return err
		}
		off = truncateAt
	}
	r.size = off
	return nil
}

func (r *Run) inboxDir() string { return filepath.Join(r.dir, paths.InboxDir) }

// repairInboxLocked appends a to_agent event for every inbox file without one (crash between
// writing the file and logging the delivery) and sets the next inbox number.
func (r *Run) repairInboxLocked() {
	msgs, _ := r.readInbox(0)
	r.nextInbox = r.st.InboxN
	for _, m := range msgs {
		if m.N > r.nextInbox {
			r.nextInbox = m.N
		}
		if r.st.inboxSeen[m.N] {
			continue
		}
		r.s.log.Warn("repairing missing to_agent event", "run", r.id, "n", m.N)
		_ = r.appendHubLocked("to_agent", map[string]any{"n": m.N, "type": m.Type, "payload": m.Payload}, m.TS)
	}
}

// ID returns the run id.
func (r *Run) ID() string { return r.id }

// AgentID returns the run's agent id.
func (r *Run) AgentID() string { return r.agentID }

// Dir returns the run directory.
func (r *Run) Dir() string { return r.dir }

// Record returns a copy of the run record.
func (r *Run) Record() proto.RunRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec
}

// Ended reports whether the run has ended (done, killed or exit stored).
func (r *Run) Ended() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st.ended
}

// LastSeq returns the highest assigned seq.
func (r *Run) LastSeq() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st.LastSeq
}

func (r *Run) tokenMatches(tok string) bool {
	r.mu.Lock()
	h := r.rec.TokenSHA256
	r.mu.Unlock()
	return ids.TokenMatches(h, tok)
}

// CheckToken reports whether tok is this run's token and it has not been revoked.
func (r *Run) CheckToken(tok string) bool {
	r.mu.Lock()
	h, revoked := r.rec.TokenSHA256, r.rec.TokenRevoked
	r.mu.Unlock()
	return ids.TokenMatches(h, tok) && !revoked
}

// TokenRevoked reports whether the run token has been revoked.
func (r *Run) TokenRevoked() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.TokenRevoked
}

// Summary returns the RunSummary for GET /api/state.
func (r *Run) Summary() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	plan := r.rec.Plan
	if r.st.Plan != nil {
		plan = r.st.Plan
	}
	sum := Summary{
		RunID: r.rec.RunID, AgentID: r.rec.AgentID, Callsign: r.rec.Callsign, AccountID: r.rec.AccountID,
		Role: r.rec.Role, Kind: r.rec.Kind, Model: r.rec.Model, Branch: r.rec.Branch, BaseBranch: r.rec.BaseBranch, Status: r.st.Status(),
		CreatedAt: r.rec.CreatedAt, EndedAt: r.rec.EndedAt, TokenRevoked: r.rec.TokenRevoked,
		Plan: append([]proto.PlanStep{}, plan...), LastSeq: r.st.LastSeq, LastEventTS: r.st.LastTS,
		OpenQuestions: append([]Question{}, r.st.Questions...), StepStates: map[string]string{},
		Merged: string(r.rec.Extra["merged"]) == "true",
	}
	for k, v := range r.st.Steps {
		sum.StepStates[k] = v
	}
	if r.st.Usage != nil {
		sum.Usage = make(map[string]any, len(r.st.Usage))
		for k, v := range r.st.Usage {
			sum.Usage[k] = v
		}
	}
	return sum
}

// Ingest parses a raw envelope line and ingests it (see IngestEnvelope).
func (r *Run) Ingest(raw []byte, src proto.Source) (seq int64, dup bool, err *proto.Error) {
	env, perr := proto.ParseIncoming(raw)
	if perr != nil {
		return 0, false, perr
	}
	return r.IngestEnvelope(env, src)
}

// IngestEnvelope validates and stores an agent message. It checks run/agent ids, validates the
// payload (hub-only types are refused), deduplicates by msg_id (a repeat returns the original
// seq and dup=true), refuses ended runs (RUN_ENDED), fills plan {confirm:true} from the run
// record, redacts secrets and appends the event.
func (r *Run) IngestEnvelope(env proto.Envelope, src proto.Source) (int64, bool, *proto.Error) {
	if env.RunID != r.id {
		return 0, false, proto.NewError(proto.CodeRunMismatch, "run_id does not match this run")
	}
	if env.AgentID != r.agentID {
		return 0, false, proto.NewError(proto.CodeAgentMismatch, "agent_id does not match this run")
	}
	payload, perr := proto.ValidateAgentPayload(env.Type, env.Payload)
	if perr != nil {
		return 0, false, perr
	}
	if env.MsgID == "" {
		env.MsgID = ids.UUID()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq, ok := r.seen[env.MsgID]; ok {
		return seq, true, nil
	}
	if r.st.ended {
		return 0, false, proto.NewError(proto.CodeRunEnded, "run has ended")
	}
	if env.Type == "plan" && payload["confirm"] == true {
		cp := make(map[string]any, len(payload)+1)
		for k, v := range payload {
			cp[k] = v
		}
		if steps, ok := cp["steps"].([]any); !ok || len(steps) == 0 {
			arr := make([]any, 0, len(r.rec.Plan))
			for _, st := range r.rec.Plan {
				arr = append(arr, map[string]any{"id": st.ID, "title": st.Title, "acceptance": st.Acceptance})
			}
			cp["steps"] = arr
		}
		payload = cp
	}
	ts := env.TS
	if ts == "" {
		ts = nowTS(time.Now())
	}
	ev := proto.StoredEvent{
		V: proto.Version, RunID: r.id, AgentID: r.agentID, TS: ts, Type: env.Type, Payload: payload,
		MsgID: env.MsgID, Src: src, ClientSeq: env.Seq, Extra: env.Extra,
	}
	if perr := r.appendLocked(&ev); perr != nil {
		return 0, false, perr
	}
	r.lastAct = time.Now()
	return ev.Seq, false, nil
}

// AppendHub stores a hub-generated event (activity, exit, stalled, ...). The payload is
// validated against hub.<type>.
func (r *Run) AppendHub(typ string, payload map[string]any) (int64, *proto.Error) {
	payload, perr := proto.ValidateHubPayload(typ, payload)
	if perr != nil {
		return 0, perr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appendHubSeqLocked(typ, payload, "")
}

func (r *Run) appendHubSeqLocked(typ string, payload map[string]any, ts string) (int64, *proto.Error) {
	if ts == "" {
		ts = nowTS(time.Now())
	}
	ev := proto.StoredEvent{
		V: proto.Version, RunID: r.id, AgentID: r.agentID, TS: ts, Type: typ, Payload: payload,
		MsgID: ids.UUID(), Src: proto.SrcHub,
	}
	if perr := r.appendLocked(&ev); perr != nil {
		return 0, perr
	}
	return ev.Seq, nil
}

func (r *Run) appendHubLocked(typ string, payload map[string]any, ts string) *proto.Error {
	_, perr := r.appendHubSeqLocked(typ, payload, ts)
	return perr
}

// redactExtra redacts the values of unknown envelope fields (they are stored and published verbatim otherwise).
func redactExtra(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, raw := range in {
		var v any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			out[k] = json.RawMessage(`"[REDACTED]"`)
			continue
		}
		b, err := json.Marshal(redact.Value(v))
		if err != nil {
			b = []byte(`"[REDACTED]"`)
		}
		out[k] = b
	}
	return out
}

// appendLocked is the only path into events.jsonl: it assigns seq, redacts, writes one line,
// updates derived state and publishes. The caller holds r.mu.
func (r *Run) appendLocked(evp *proto.StoredEvent) *proto.Error {
	ev := *evp
	if r.closed || r.f == nil {
		return proto.NewError(proto.CodeInternal, "run store is closed")
	}
	ev.Seq = r.st.LastSeq + 1
	if red, ok := redact.Value(ev.Payload).(map[string]any); ok {
		ev.Payload = red
	}
	ev.Extra = redactExtra(ev.Extra)
	line := proto.MarshalLine(ev)
	if line == nil {
		return proto.NewError(proto.CodeInternal, "cannot encode event")
	}
	n, err := r.f.Write(line)
	if err != nil || n != len(line) {
		if n > 0 { // best effort: drop the partial line so the log stays parsable
			_ = r.f.Truncate(r.size)
		}
		r.s.log.Error("events.jsonl write failed", "run", r.id, "err", err)
		return proto.NewError(proto.CodeInternal, "storage error")
	}
	*evp = ev
	r.idx = append(r.idx, idxEntry{ev.Seq, r.size})
	r.size += int64(len(line))
	r.seen[ev.MsgID] = ev.Seq
	wasEnded := r.st.ended
	r.st.Apply(ev)
	if r.st.ended && !wasEnded {
		r.rec.TokenRevoked = true // A10
		r.rec.EndedAt = ev.TS
		_ = r.f.Sync()
	}
	if r.st.Status() != r.savedStat || (r.st.ended && !wasEnded) {
		r.saveRecordLocked()
	}
	if pub := r.s.opts.Publish; pub != nil {
		pub(ev)
	}
	return nil
}

func (r *Run) saveRecordLocked() error {
	r.rec.Status = r.st.Status()
	b, err := json.MarshalIndent(r.rec, "", "  ")
	if err == nil {
		err = WriteFileAtomic(filepath.Join(r.dir, paths.RunRecordFile), b, 0o644)
	}
	if err != nil {
		r.s.log.Error("run.json write failed", "run", r.id, "err", err)
		return err
	}
	r.savedStat = r.rec.Status
	return nil
}

// SetExtra stores an unknown-to-the-schema field in run.json (kept across restarts), e.g.
// "merged": true. A nil value removes the key. Core record fields cannot be overwritten.
func (r *Run) SetExtra(key string, v any) error {
	if key == "" {
		return errors.New("store: empty extra key")
	}
	if protoKnownKey(key) {
		return fmt.Errorf("store: %q is a core run.json field", key)
	}
	var raw json.RawMessage
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw = b
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := make(map[string]json.RawMessage, len(r.rec.Extra)+1)
	for k, x := range r.rec.Extra {
		m[k] = x
	}
	if raw == nil {
		delete(m, key)
	} else {
		m[key] = raw
	}
	r.rec.Extra = m
	return r.saveRecordLocked()
}

func protoKnownKey(k string) bool {
	switch k {
	case "run_id", "agent_id", "callsign", "account_id", "role", "kind", "model", "effort", "task", "repo",
		"branch", "base_branch", "worktree", "actor", "status", "created_at", "ended_at", "token_sha256",
		"token_revoked", "plan":
		return true
	}
	return false
}

// Events returns stored events with seq > since, in order.
func (r *Run) Events(since int64) ([]proto.StoredEvent, error) {
	r.mu.Lock()
	i := sort.Search(len(r.idx), func(i int) bool { return r.idx[i].seq > since })
	if i >= len(r.idx) {
		r.mu.Unlock()
		return []proto.StoredEvent{}, nil
	}
	start, end := r.idx[i].off, r.size
	r.mu.Unlock()
	f, err := os.Open(filepath.Join(r.dir, paths.EventsFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(io.LimitReader(f, end-start), 64*1024)
	out := []proto.StoredEvent{}
	for {
		line, rerr := br.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			var ev proto.StoredEvent
			if err := json.Unmarshal(t, &ev); err == nil && ev.Seq > since {
				out = append(out, ev)
			}
		}
		if rerr != nil {
			break
		}
	}
	return out, nil
}

// Inbox returns inbox messages with n > after, ordered by n.
func (r *Run) Inbox(after int) ([]proto.InboxMessage, error) { return r.readInbox(after) }

func (r *Run) readInbox(after int) ([]proto.InboxMessage, error) {
	entries, err := os.ReadDir(r.inboxDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []proto.InboxMessage{}, nil
		}
		return nil, err
	}
	type nf struct {
		n    int
		name string
	}
	var files []nf
	for _, e := range entries {
		if n, ok := paths.ParseInboxFileName(e.Name()); ok && n > after {
			files = append(files, nf{n, e.Name()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].n < files[j].n })
	out := []proto.InboxMessage{}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(r.inboxDir(), f.name))
		if err != nil {
			continue
		}
		var m proto.InboxMessage
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil || m.N != f.n {
			continue
		}
		if m.Payload == nil {
			m.Payload = map[string]any{}
		}
		out = append(out, m)
	}
	return out, nil
}

// Deliver validates a hub-to-agent message, writes inbox/<n>.json atomically and records a
// to_agent event. It returns the inbox number n.
func (r *Run) Deliver(typ string, payload map[string]any) (int, *proto.Error) {
	payload, perr := proto.ValidateInboxPayload(typ, payload)
	if perr != nil {
		return 0, perr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, proto.NewError(proto.CodeInternal, "run store is closed")
	}
	if r.st.ended {
		return 0, proto.NewError(proto.CodeRunEnded, "run has ended")
	}
	if typ == "answer" {
		// An answer must match an open question; otherwise a blocked agent would wait for its
		// timeout while the caller believes it answered.
		qid, _ := payload["qid"].(string)
		if !r.st.hasOpenQuestion(qid) {
			return 0, proto.NewError(proto.CodeUnknownQID, "no open question with qid "+qid+" in this run")
		}
	}
	n := r.nextInbox + 1
	ts := nowTS(time.Now())
	msg := proto.InboxMessage{V: proto.Version, N: n, TS: ts, Type: typ, Payload: payload}
	if err := os.MkdirAll(r.inboxDir(), 0o700); err != nil {
		return 0, proto.NewError(proto.CodeInternal, "cannot create inbox")
	}
	if err := WriteFileAtomic(filepath.Join(r.inboxDir(), paths.InboxFileName(n)), proto.MarshalLine(msg), 0o644); err != nil {
		r.s.log.Error("inbox write failed", "run", r.id, "err", err)
		return 0, proto.NewError(proto.CodeInternal, "storage error")
	}
	r.nextInbox = n
	if perr := r.appendHubLocked("to_agent", map[string]any{"n": n, "type": typ, "payload": payload}, ts); perr != nil {
		return 0, perr
	}
	return n, nil
}

// LastActivity returns when the run last received an agent (non-hub) event, or when it was
// opened if it has none yet.
func (r *Run) LastActivity() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastAct
}

// CheckStalled emits a hub stalled event if the run is not ended, not already stalled and has
// had no agent event for timeout. It reports whether it emitted. The next agent event clears
// the stalled status; a stall never ends the run (A4).
func (r *Run) CheckStalled(now time.Time, timeout time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.st.ended || r.st.stalled || timeout <= 0 || now.Sub(r.lastAct) < timeout {
		return false
	}
	payload := map[string]any{"last_seen": nowTS(r.lastAct), "timeout_s": timeout.Seconds()}
	return r.appendHubLocked("stalled", payload, "") == nil
}

// Close syncs and closes the event log. Further appends fail. Idempotent.
func (r *Run) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var err error
	if r.f != nil {
		err = r.f.Sync()
		if cerr := r.f.Close(); err == nil {
			err = cerr
		}
		r.f = nil
	}
	r.saveRecordLocked()
	return err
}

// AppendAdapter stores an adapter-derived event (activity, usage) with a stable msg_id, so
// re-reading the same agent output never duplicates it (dup=true returns the original seq).
func (r *Run) AppendAdapter(typ string, payload map[string]any, msgID string) (int64, bool, *proto.Error) {
	payload, perr := proto.ValidateHubPayload(typ, payload)
	if perr != nil {
		return 0, false, perr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq, ok := r.seen[msgID]; ok {
		return seq, true, nil
	}
	ev := proto.StoredEvent{V: proto.Version, RunID: r.id, AgentID: r.agentID, TS: nowTS(time.Now()),
		Type: typ, Payload: payload, MsgID: msgID, Src: proto.SrcAdapter}
	if perr := r.appendLocked(&ev); perr != nil {
		return 0, false, perr
	}
	return ev.Seq, false, nil
}
