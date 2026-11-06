package bus

import (
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/proto"
)

func ev(seq int64) proto.StoredEvent { return proto.StoredEvent{Seq: seq, Type: "heartbeat"} }

func TestReplayAndReset(t *testing.T) {
	b := New(5, 100)
	for i := 1; i <= 8; i++ { // ring holds ids 4..8
		b.Publish(ev(int64(i)))
	}
	// replay from inside the window
	sub, replay, reset := b.Subscribe(5, true)
	defer sub.Close()
	if reset || len(replay) != 3 || replay[0].N != 6 || replay[2].N != 8 {
		t.Fatalf("reset=%v replay=%v", reset, replay)
	}
	// exactly at the edge: lastID 3 -> needs 4.. which is available
	_, replay, reset = b.Subscribe(3, true)
	if reset || len(replay) != 5 {
		t.Fatalf("edge: reset=%v len=%d", reset, len(replay))
	}
	// too old
	_, replay, reset = b.Subscribe(2, true)
	if !reset || len(replay) != 0 {
		t.Fatalf("old: reset=%v len=%d", reset, len(replay))
	}
	// future id (hub restarted)
	_, replay, reset = b.Subscribe(99, true)
	if !reset || len(replay) != 0 {
		t.Fatalf("future: reset=%v", reset)
	}
	// caught up
	_, replay, reset = b.Subscribe(8, true)
	if reset || len(replay) != 0 {
		t.Fatalf("caught up: reset=%v", reset)
	}
	// no Last-Event-ID: no replay, no reset
	_, replay, reset = b.Subscribe(0, false)
	if reset || len(replay) != 0 {
		t.Fatal("fresh subscribe")
	}
	// live delivery continues in order after replay
	b.Publish(ev(9))
	if it := <-sub.C; it.N != 9 {
		t.Fatalf("live item %d", it.N)
	}
}

func TestEmptyBrokerLastID(t *testing.T) {
	b := New(5, 10)
	if _, _, reset := b.Subscribe(0, true); reset {
		t.Fatal("0 on empty broker is fine")
	}
	if _, _, reset := b.Subscribe(1, true); !reset {
		t.Fatal("future id on empty broker must reset")
	}
}

func TestSlowSubscriberDroppedWithoutBlocking(t *testing.T) {
	b := New(100, 4)
	slow, _, _ := b.Subscribe(0, false)
	fast, _, _ := b.Subscribe(0, false)
	done := make(chan struct{})
	go func() {
		for i := 1; i <= 50; i++ {
			b.Publish(ev(int64(i)))
			select { // fast reader keeps up
			case <-fast.C:
			default:
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	select {
	case <-slow.Dropped:
	default:
		t.Fatal("slow subscriber should be dropped")
	}
	select {
	case <-fast.Dropped:
		t.Fatal("fast subscriber must stay")
	default:
	}
}

func TestClose(t *testing.T) {
	b := New(5, 5)
	s, _, _ := b.Subscribe(0, false)
	b.Close()
	b.Close()
	select {
	case <-b.Closed:
	default:
		t.Fatal("Closed not closed")
	}
	s.Close()
	s2, _, _ := b.Subscribe(0, false)
	select {
	case <-s2.Dropped:
	default:
		t.Fatal("subscribe after close should be dropped")
	}
}
