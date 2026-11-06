// Package audit is the append-only log of config changes and other consequential actions
// (<dataDir>/audit.jsonl, one JSON object per line).
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/redact"
)

// FileName is the log file inside the data dir.
const FileName = "audit.jsonl"

// Actors.
const (
	ActorHuman     = "human"
	ActorConductor = "conductor"
	ActorHub       = "hub"
)

// Entry is one audit record.
type Entry struct {
	TS     string         `json:"ts"`
	Actor  string         `json:"actor"` // human | conductor | hub
	Action string         `json:"action"`
	Target string         `json:"target,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
}

// Log is an append-only audit log, safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// Open opens (creating if needed) <dataDir>/audit.jsonl for appending.
func Open(dataDir string) (*Log, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dataDir, FileName)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{path: p, f: f}, nil
}

// Path returns the log file path.
func (l *Log) Path() string { return l.path }

// Append writes e as one line. TS is filled (UTC, ms) when empty; Detail is redacted.
func (l *Log) Append(e Entry) error {
	if e.TS == "" {
		e.TS = proto.NowTS()
	}
	if e.Detail != nil {
		if d, ok := redact.Value(e.Detail).(map[string]any); ok {
			e.Detail = d
		}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return errors.New("audit: log is closed")
	}
	if _, err := l.f.Write(b); err != nil {
		return err
	}
	return l.f.Sync()
}

// Read returns up to limit of the newest entries, oldest first (newest last). limit <= 0
// means all. Unparseable lines are skipped.
func (l *Log) Read(limit int) ([]Entry, error) {
	l.mu.Lock()
	data, err := os.ReadFile(l.path)
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) == nil {
			out = append(out, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// Close closes the log. Further Appends fail.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
