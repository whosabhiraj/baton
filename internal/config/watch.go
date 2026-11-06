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

	reloadMu    sync.Mutex // serialises Reload (poller and callers)
	globalPath  string
	projectPath string
	last        [2]fileSig
	onChange    func(*Snapshot)
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
	w := &Watcher{cancel: cancel, done: make(chan struct{}), globalPath: globalPath, projectPath: projectPath, onChange: onChange}
	g, p := readSource(globalPath), readSource(projectPath)
	w.cur = build(g, p)
	w.last = [2]fileSig{sigOf(g), sigOf(p)}
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
			w.Reload()
		}
	}()
	return w
}

// Reload re-reads both files now. If they changed since the last look the snapshot is
// replaced (a failed load keeps the last good Config and Version) and onChange is called.
// It returns the current snapshot either way.
func (w *Watcher) Reload() *Snapshot {
	w.reloadMu.Lock()
	defer w.reloadMu.Unlock()
	g, p := readSource(w.globalPath), readSource(w.projectPath)
	sig := [2]fileSig{sigOf(g), sigOf(p)}
	if sig == w.last {
		return w.Current()
	}
	w.last = sig
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
	if w.onChange != nil {
		w.onChange(next)
	}
	return next
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
