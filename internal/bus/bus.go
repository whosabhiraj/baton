// Package bus is the in-process event broker behind GET /api/stream: a global, per-hub-instance
// event counter, a ring buffer for Last-Event-ID replay, and non-blocking fan-out to
// subscribers (slow ones are dropped, never waited for).
package bus

import (
	"sync"

	"github.com/whosabhiraj/baton/internal/proto"
)

// Defaults.
const (
	DefaultRing      = 5000
	DefaultSubBuffer = 1024
)

// Item is one broker event with its global id.
type Item struct {
	N  uint64
	Ev proto.StoredEvent
}

// Sub is a subscription.
type Sub struct {
	// C delivers live items in order. It is never closed; watch Dropped and Closed.
	C chan Item
	// Dropped is closed when the subscriber fell behind and was removed. Items already in C
	// may still be read; the stream should then tell the client to reset.
	Dropped chan struct{}
	b       *Broker
	once    sync.Once
}

// Close unsubscribes. Safe to call more than once.
func (s *Sub) Close() { s.b.remove(s) }

// Broker fans events out to subscribers.
type Broker struct {
	mu     sync.Mutex
	n      uint64
	ring   []Item // circular, len <= cap
	head   int    // index of the oldest item
	count  int
	subs   map[*Sub]struct{}
	subBuf int
	closed bool
	// Closed is closed by Close.
	Closed chan struct{}
}

// New creates a broker with the given ring size and per-subscriber buffer (<=0 = defaults).
func New(ring, subBuffer int) *Broker {
	if ring <= 0 {
		ring = DefaultRing
	}
	if subBuffer <= 0 {
		subBuffer = DefaultSubBuffer
	}
	return &Broker{ring: make([]Item, ring), subs: map[*Sub]struct{}{}, subBuf: subBuffer, Closed: make(chan struct{})}
}

// Publish assigns the next global id, stores the event in the ring and delivers it to every
// subscriber without blocking; a subscriber whose buffer is full is dropped.
func (b *Broker) Publish(ev proto.StoredEvent) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	it := Item{N: b.n, Ev: ev}
	if b.count < len(b.ring) {
		b.ring[(b.head+b.count)%len(b.ring)] = it
		b.count++
	} else {
		b.ring[b.head] = it
		b.head = (b.head + 1) % len(b.ring)
	}
	for s := range b.subs {
		select {
		case s.C <- it:
		default:
			delete(b.subs, s)
			s.once.Do(func() { close(s.Dropped) })
		}
	}
	return it.N
}

// LastID returns the id of the most recently published event (0 = none yet).
func (b *Broker) LastID() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

// Subscribe registers a subscriber. If hasLast, lastID is the client's Last-Event-ID: events
// after it are returned as replay (atomically with registration, so none are lost or
// duplicated). If lastID is older than the ring or newer than anything published (the hub
// restarted), reset is true and no replay is returned: the client must re-fetch /api/state.
func (b *Broker) Subscribe(lastID uint64, hasLast bool) (sub *Sub, replay []Item, reset bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub = &Sub{C: make(chan Item, b.subBuf), Dropped: make(chan struct{}), b: b}
	if hasLast {
		oldest := b.n - uint64(b.count) + 1 // id of the oldest buffered item (n+1 when empty)
		switch {
		case lastID > b.n:
			reset = true
		case lastID+1 < oldest:
			reset = true
		default:
			for i := 0; i < b.count; i++ {
				it := b.ring[(b.head+i)%len(b.ring)]
				if it.N > lastID {
					replay = append(replay, it)
				}
			}
		}
	}
	if b.closed {
		sub.once.Do(func() { close(sub.Dropped) })
	} else {
		b.subs[sub] = struct{}{}
	}
	return sub, replay, reset
}

func (b *Broker) remove(s *Sub) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

// Close drops every subscriber's registration and closes Closed. Idempotent.
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	close(b.Closed)
	b.subs = map[*Sub]struct{}{}
}
