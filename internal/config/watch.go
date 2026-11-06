package config

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"
)

// Watcher polls both files and reloads on change.
type Watcher struct {
	mu     sync.RWMutex
	cur    *Snapshot
	cancel context.CancelFunc
	done   chan struct{}
}

type fileSig struct {
	present bool
	size    int
	sum     [sha256.Size]byte
}

func sigOf(s source) fileSig {
	return fileSig{present: s.present, size: len(s.data), sum: sha256.Sum256(s.data)}
}

// Watch loads once, then polls every interval (default 2 s). onChange is called after each
// reload attempt that changed the files (including failed ones: check Issues). A failed
// reload keeps the last good Config and Version. The initial load does not call onChange.
func Watch(ctx context.Context, globalPath, projectPath string, interval time.Duration, onChange func(*Snapshot)) *Watcher {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	w := &Watcher{cancel: cancel, done: make(chan struct{})}
	g, p := readSource(globalPath), readSource(projectPath)
	w.cur = build(g, p)
	last := [2]fileSig{sigOf(g), sigOf(p)}
	go func() {
		defer close(w.done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			g, p := readSource(globalPath), readSource(projectPath)
			sig := [2]fileSig{sigOf(g), sigOf(p)}
			if sig == last {
				continue
			}
			last = sig
			next := build(g, p)
			w.mu.Lock()
			prev := w.cur
			if next.OK() {
				next.Version = prev.Version + 1
			} else {
				next.Config = prev.Config
				next.Version = prev.Version
			}
			w.cur = next
			w.mu.Unlock()
			if onChange != nil {
				onChange(next)
			}
		}
	}()
	return w
}

// Current returns the latest snapshot (treat as read-only).
func (w *Watcher) Current() *Snapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cur
}

// Stop ends polling and waits for the poller to exit. Safe to call more than once.
func (w *Watcher) Stop() {
	if w == nil || w.cancel == nil {
		return
	}
	w.cancel()
	<-w.done
}
