package spool

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/store"
)

// WarnFunc receives a warning about a skipped outbox line.
type WarnFunc func(runID, code, msg string, offset int64)

// NewOutboxTailer builds the tailer for one run's outbox.jsonl. Each line is parsed as an
// envelope and ingested with src "spool"; a line without msg_id gets spool@<byte offset>
// (A3). Rejected lines (bad JSON, wrong run/agent, hub-only type, invalid payload, run ended)
// are skipped and reported through warn; the offset still advances past them.
func NewOutboxTailer(run *store.Run, warn WarnFunc) *Tailer {
	t := &Tailer{
		Path:       filepath.Join(run.Dir(), paths.OutboxFile),
		OffsetPath: filepath.Join(run.Dir(), paths.OutboxOffsetFile),
	}
	w := func(code, msg string, off int64) {
		if warn != nil {
			warn(run.ID(), code, msg, off)
		}
	}
	t.Warn = func(code, msg string, off int64) { w(code, msg, off) }
	t.Handle = func(line []byte, off int64) error {
		env, perr := proto.ParseIncoming(line)
		if perr != nil {
			w(perr.Code, perr.Msg, off)
			return nil
		}
		if env.MsgID == "" {
			env.MsgID = fmt.Sprintf("spool@%d", off)
		}
		if _, _, perr := run.IngestEnvelope(env, proto.SrcSpool); perr != nil {
			if perr.Code == proto.CodeInternal {
				return perr // storage trouble: stop, retry next poll
			}
			w(perr.Code, perr.Msg, off)
		}
		return nil
	}
	return t
}

// Manager polls the outbox of every run in a store.
type Manager struct {
	st       *store.Store
	interval time.Duration
	log      *slog.Logger
	warn     WarnFunc

	mu      sync.Mutex
	tailers map[string]*Tailer
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewManager creates a manager. warn may be nil; log must not be nil.
func NewManager(st *store.Store, interval time.Duration, log *slog.Logger, warn WarnFunc) *Manager {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	return &Manager{st: st, interval: interval, log: log, warn: warn, tailers: map[string]*Tailer{}}
}

// DrainAll polls every run once, synchronously (used at startup before the listener opens and
// once more at shutdown).
func (m *Manager) DrainAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.st.List() {
		t := m.tailers[r.ID()]
		if t == nil {
			t = NewOutboxTailer(r, m.warn)
			m.tailers[r.ID()] = t
		}
		if t.Done && r.Ended() {
			continue
		}
		if err := t.Poll(); err != nil {
			m.log.Warn("outbox poll failed", "run", r.ID(), "err", err)
		}
	}
}

// Start begins polling in a background goroutine until Stop.
func (m *Manager) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		tick := time.NewTicker(m.interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				m.safeDrain()
			}
		}
	}()
}

func (m *Manager) safeDrain() {
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("spool panic recovered", "panic", fmt.Sprint(p))
		}
	}()
	m.DrainAll()
}

// Stop ends polling and waits for the goroutine.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}
