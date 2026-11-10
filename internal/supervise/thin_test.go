package supervise

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestThinWriter(t *testing.T) {
	var out bytes.Buffer
	tw := newThinWriter(&out, []string{"stream_event", "delta"})
	now := time.Unix(1000, 0)
	tw.now = func() time.Time { return now }

	delta := `{"type":"stream_event","event":{"type":"content_block_delta"}}` + "\n"
	codex := `{"method":"item/agentMessage/delta","params":{}}` + "\n"
	msg := `{"type":"assistant","message":{"content":[]}}` + "\n"
	notJSON := "delta but not json\n"

	// split writes: lines are only judged once complete
	tw.Write([]byte(delta[:10]))
	tw.Write([]byte(delta[10:] + delta + codex + msg + notJSON))
	now = now.Add(2 * time.Second)
	tw.Write([]byte(delta + msg))
	now = now.Add(4 * time.Second)
	tw.Write([]byte(codex + "tail without newline"))
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	want := delta + msg + notJSON + msg + codex + "tail without newline"
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
	if strings.Count(out.String(), "stream_event") != 1 {
		t.Fatal("deltas not thinned")
	}

	// no suffixes: everything passes
	out.Reset()
	all := newThinWriter(&out, nil)
	all.Write([]byte(delta + delta))
	if out.String() != delta+delta {
		t.Fatalf("%q", out.String())
	}
}
