package proto

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseIncoming(t *testing.T) {
	env, e := ParseIncoming([]byte(`{"v":1,"run_id":"r","agent_id":"a","type":"progress","seq":7,"payload":{"percent":12345678901234567890,"note":"<b>&"},"msg_id":"m","trace":{"x":[1,2]}}`))
	if e != nil {
		t.Fatal(e)
	}
	if env.Seq == nil || *env.Seq != 7 || env.MsgID != "m" || env.V != 1 {
		t.Errorf("fields: %+v", env)
	}
	if n, ok := env.Payload["percent"].(json.Number); !ok || n.String() != "12345678901234567890" {
		t.Errorf("number not preserved: %#v", env.Payload["percent"])
	}
	if string(env.Extra["trace"]) != `{"x":[1,2]}` {
		t.Errorf("extra: %v", env.Extra)
	}
	out := string(MarshalLine(env))
	want := `{"v":1,"run_id":"r","agent_id":"a","seq":7,"type":"progress","payload":{"note":"<b>&","percent":12345678901234567890},"msg_id":"m","trace":{"x":[1,2]}}` + "\n"
	if out != want {
		t.Errorf("marshal:\n got %s\nwant %s", out, want)
	}
}

func TestParseIncomingDefaultsAndErrors(t *testing.T) {
	env, e := ParseIncoming([]byte(`{"v":1,"run_id":"r","agent_id":"a","type":"x"}`))
	if e != nil || env.Payload == nil || len(env.Payload) != 0 {
		t.Fatalf("payload default: %+v %v", env, e)
	}
	for _, bad := range []string{``, `{`, `[]`, `"s"`, `{"v":1,"run_id":"r","agent_id":"a","type":"x"} extra`, `{"v":2,"run_id":"r","agent_id":"a","type":"x"}`, `{"v":1,"run_id":"r","agent_id":"a","type":"x","payload":null}`} {
		if _, e := ParseIncoming([]byte(bad)); e == nil || e.Code != CodeInvalidEnvelope || e.Status != 400 {
			t.Errorf("%q: got %v", bad, e)
		}
	}
}

func TestValidateAgentPayload(t *testing.T) {
	if _, e := ValidateAgentPayload("exit", nil); e == nil || e.Code != CodeHubOnlyType {
		t.Errorf("hub-only: %v", e)
	}
	p, e := ValidateAgentPayload("custom_future", nil)
	if e != nil || p == nil {
		t.Errorf("unknown type should pass with {}: %v %v", p, e)
	}
	if _, e := ValidateAgentPayload("step_done", map[string]any{"step_id": "s"}); e == nil || e.Code != CodeInvalidPayload || !strings.Contains(e.Msg, "summary") {
		t.Errorf("invalid: %v", e)
	}
	if _, e := ValidateAgentPayload("heartbeat", nil); e != nil {
		t.Errorf("nil heartbeat payload: %v", e)
	}
	if _, e := ValidateAgentPayload("progress", map[string]any{"percent": json.Number("50"), "note": "n"}); e != nil {
		t.Errorf("json.Number: %v", e)
	}
	if _, e := ValidateAgentPayload("progress", map[string]any{"percent": 50, "note": "n"}); e != nil {
		t.Errorf("int: %v", e)
	}
}

func TestValidateInboxHub(t *testing.T) {
	if _, e := ValidateInboxPayload("nope", nil); e == nil || e.Code != CodeUnknownInboxType {
		t.Errorf("unknown inbox: %v", e)
	}
	if _, e := ValidateInboxPayload("answer", map[string]any{"qid": "q"}); e == nil || e.Code != CodeInvalidPayload {
		t.Errorf("answer: %v", e)
	}
	if _, e := ValidateInboxPayload("stop", nil); e != nil {
		t.Errorf("stop: %v", e)
	}
	if _, e := ValidateHubPayload("stalled", map[string]any{"last_seen": "x", "timeout_s": 1}); e != nil {
		t.Errorf("stalled: %v", e)
	}
	if _, e := ValidateHubPayload("heartbeat", nil); e == nil {
		t.Error("non-hub type should fail")
	}
}

func TestErrorType(t *testing.T) {
	want := map[string]int{CodeInvalidEnvelope: 400, CodeInvalidPayload: 400, CodeHubOnlyType: 400, CodeUnknownInboxType: 400,
		CodeUnauthorized: 401, CodeRunMismatch: 403, CodeAgentMismatch: 403, CodeNotFound: 404, CodeRunEnded: 410, CodeInternal: 500}
	for c, s := range want {
		if e := NewError(c, "m"); e.Status != s || e.Error() != c+": m" {
			t.Errorf("%s: %+v", c, e)
		}
	}
	var err error = NewError(CodeNotFound, "x")
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != CodeNotFound {
		t.Error("errors.As")
	}
}

func TestStoredEventRoundTrip(t *testing.T) {
	in := `{"v":1,"run_id":"r","agent_id":"a","seq":3,"ts":"t","type":"x","payload":{"n":1.50},"msg_id":"m","src":"hub","client_seq":9,"zz":true,"aa":"<&>"}`
	var ev StoredEvent
	if err := json.Unmarshal([]byte(in), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Src != SrcHub || ev.ClientSeq == nil || *ev.ClientSeq != 9 || len(ev.Extra) != 2 {
		t.Fatalf("%+v", ev)
	}
	got := strings.TrimSpace(string(MarshalLine(ev)))
	want := `{"v":1,"run_id":"r","agent_id":"a","seq":3,"ts":"t","type":"x","payload":{"n":1.50},"msg_id":"m","src":"hub","client_seq":9,"aa":"<&>","zz":true}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// nil payload is written as {}.
	if s := string(MarshalLine(StoredEvent{V: 1})); !strings.Contains(s, `"payload":{}`) {
		t.Errorf("nil payload: %s", s)
	}
	// Valid against the stored schema.
	var v any
	_ = json.Unmarshal([]byte(got), &v)
	if err := ValidateJSON("envelope.stored", v); err != nil {
		t.Error(err)
	}
}

func TestRunRecordRoundTrip(t *testing.T) {
	r := RunRecord{RunID: "r_1", AgentID: "a", Status: StatusRunning, CreatedAt: "t", TokenSHA256: "h"}
	b := MarshalLine(r)
	if !strings.Contains(string(b), `"plan":[]`) || strings.Contains(string(b), "callsign") {
		t.Errorf("%s", b)
	}
	var v any
	_ = json.Unmarshal(b, &v)
	if err := ValidateJSON("run-record", v); err != nil {
		t.Error(err)
	}
	r.Extra = map[string]json.RawMessage{"future": json.RawMessage(`{"a":1}`)}
	r.Plan = []PlanStep{{ID: "s", Title: "t", Acceptance: "a"}}
	var back RunRecord
	if err := json.Unmarshal(MarshalLine(r), &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, r) {
		t.Errorf("round trip:\n%+v\n%+v", back, r)
	}
}

func TestInboxMessageMarshal(t *testing.T) {
	m := InboxMessage{V: 1, N: 2, TS: "t", Type: "message", Payload: map[string]any{"text": "a<b"}}
	if got := string(MarshalLine(m)); got != `{"v":1,"n":2,"ts":"t","type":"message","payload":{"text":"a<b"}}`+"\n" {
		t.Error(got)
	}
	if MarshalLine(make(chan int)) != nil {
		t.Error("unencodable should give nil")
	}
}

func TestIsHubOnly(t *testing.T) {
	for _, h := range HubOnlyTypes {
		if !IsHubOnly(h) {
			t.Error(h)
		}
	}
	if IsHubOnly("hello") || IsHubOnly("") {
		t.Error("false positive")
	}
}
