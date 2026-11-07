package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosabhiraj/baton/internal/paths"
	"github.com/whosabhiraj/baton/internal/proto"
)

func TestErrPathsRedactionNeverReachesEventsFile(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	r := newRun(t, s)
	secrets := []string{
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789",
		"bt_run_abcdefghijklmnop1234",
		"hunter2hunter2",
	}
	ing := func(id, typ, payload string) {
		t.Helper()
		raw := `{"v":1,"run_id":"r_TEST","agent_id":"a1","type":"` + typ + `","msg_id":"` + id + `","payload":` + payload + `}`
		if _, _, e := r.Ingest([]byte(raw), proto.SrcCLI); e != nil {
			t.Fatalf("%s: %v", typ, e)
		}
	}
	ing("m1", "progress", `{"percent":10,"note":"using `+secrets[0]+` now"}`)
	ing("m2", "error", `{"message":"auth failed with `+secrets[1]+`","fatal":false}`)
	ing("m3", "artifact", `{"kind":"file","ref":"x","note":"password=`+secrets[2]+`"}`)
	if _, _, e := r.AppendAdapter("progress", map[string]any{"percent": 1.0, "note": "adapter " + secrets[0]}, "ad1"); e != nil {
		t.Log("adapter append:", e)
	}

	evs, err := r.Events(0)
	if err != nil || len(evs) < 3 {
		t.Fatalf("events %d err %v", len(evs), err)
	}
	raw, err := os.ReadFile(filepath.Join(paths.RunDir(dir, "r_TEST"), paths.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range secrets {
		if strings.Contains(string(raw), sec) {
			t.Fatalf("secret %q reached events.jsonl:\n%s", sec, raw)
		}
		for _, ev := range evs {
			if strings.Contains(string(proto.MarshalLine(ev)), sec) {
				t.Fatalf("secret %q served by Events()", sec)
			}
		}
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("no redaction marker in events.jsonl:\n%s", raw)
	}
}

func TestErrPathsDeliverErrors(t *testing.T) {
	s := openStore(t, t.TempDir())
	r := newRun(t, s)
	if _, e := r.Deliver("nope", map[string]any{}); e == nil || e.Code != proto.CodeUnknownInboxType || e.Status != 400 {
		t.Fatalf("unknown inbox type: %v", e)
	}
	if _, e := r.Deliver("answer", map[string]any{"qid": "q_9", "text": "x"}); e == nil || e.Code != proto.CodeUnknownQID || e.Status != 409 {
		t.Fatalf("unknown qid: %v", e)
	}
	if _, e := r.Deliver("message", map[string]any{}); e == nil || e.Code != proto.CodeInvalidPayload {
		t.Fatalf("invalid payload: %v", e)
	}
	// hub-only agent types cannot be ingested; ended runs refuse deliveries and ingests
	if _, _, e := r.IngestEnvelope(proto.Envelope{V: 1, RunID: "r_TEST", AgentID: "a1", Type: "exit", MsgID: "e1", Payload: map[string]any{}}, proto.SrcCLI); e == nil || e.Code != proto.CodeHubOnlyType {
		t.Fatalf("hub only: %v", e)
	}
	if _, _, e := r.IngestEnvelope(proto.Envelope{V: 1, RunID: "r_TEST", AgentID: "a1", Type: "done", MsgID: "d1",
		Payload: map[string]any{"status": "success", "summary": "ok"}}, proto.SrcCLI); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Deliver("message", map[string]any{"text": "hi", "from": "human"}); e == nil || e.Code != proto.CodeRunEnded || e.Status != 410 {
		t.Fatalf("deliver after end: %v", e)
	}
	if _, _, e := r.IngestEnvelope(hb("late"), proto.SrcCLI); e == nil || e.Code != proto.CodeRunEnded {
		t.Fatalf("ingest after end: %v", e)
	}
}
