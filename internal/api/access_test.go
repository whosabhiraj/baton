package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/api"
	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/audit"
	"github.com/whosabhiraj/baton/internal/proto"
)

const condTok = "bt_cond_test"

type accessEnv struct {
	*env
	ap *approvals.Store
	au *audit.Log
}

func newAccessEnv(t *testing.T) *accessEnv {
	t.Helper()
	dir := t.TempDir()
	ap, err := approvals.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	au, err := audit.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = au.Close() })
	e := newEnv(t, func(d *api.Deps) {
		d.Approvals, d.Audit = ap, au
		d.CheckConductor = func(tok string) bool { return tok == condTok }
	})
	return &accessEnv{e, ap, au}
}

func (e *accessEnv) create(runTok, run, body string) (int, map[string]any) {
	return e.do("POST", "/api/runs/"+run+"/access", runTok, body)
}

func apID(m map[string]any) string { return m["approval"].(map[string]any)["id"].(string) }

const accBody = `{"kind":"command","summary":"curl example.com","detail":{"command":"curl example.com","cwd":"/w","reason":"fetch"}}`

func TestAccessLifecycle(t *testing.T) {
	e := newAccessEnv(t)
	tok := e.newRun("r1")
	st, m := e.create(tok, "r1", accBody)
	if st != 200 {
		t.Fatalf("create: %d %v", st, m)
	}
	ap := m["approval"].(map[string]any)
	d := ap["detail"].(map[string]any)
	if ap["kind"] != "access" || ap["status"] != "pending" || ap["run_id"] != "r1" || ap["requested_by"] != "agent" ||
		d["kind"] != "command" || d["summary"] != "curl example.com" || d["agent_id"] != "a-r1" || d["cwd"] != "/w" ||
		!strings.HasSuffix(ap["title"].(string), ": curl example.com") {
		t.Fatalf("approval: %v", ap)
	}
	id := apID(m)
	// pending poll times out without a decision
	t0 := time.Now()
	st, m = e.do("GET", "/api/runs/r1/access/"+id+"?wait=0.3", tok, "")
	if st != 200 || m["status"] != "pending" || time.Since(t0) < 250*time.Millisecond {
		t.Fatalf("poll timeout: %d %v", st, m)
	}
	// long poll wakes on approve; approving has no other effect
	go func() {
		time.Sleep(100 * time.Millisecond)
		e.do("POST", "/api/approvals/"+id+"/approve", adminTok, `{"note":"ok"}`)
	}()
	t0 = time.Now()
	st, m = e.do("GET", "/api/runs/r1/access/"+id+"?wait=10", tok, "")
	if st != 200 || m["status"] != "approved" || m["note"] != "ok" || time.Since(t0) > 3*time.Second {
		t.Fatalf("poll approve: %d %v", st, m)
	}
	if r := e.st.Get("r1"); r.Ended() {
		t.Fatal("approve changed the run")
	}
	// admin may poll too; unknown approval = 404
	if st, m = e.do("GET", "/api/runs/r1/access/"+id, adminTok, ""); st != 200 || m["status"] != "approved" {
		t.Fatalf("admin poll: %d %v", st, m)
	}
	e.expectErr("GET", "/api/runs/r1/access/ap_nope", tok, "", 404, "NOT_FOUND")
	// reject
	_, m = e.create(tok, "r1", accBody)
	id2 := apID(m)
	e.do("POST", "/api/approvals/"+id2+"/reject", adminTok, `{"note":"no"}`)
	if _, m = e.do("GET", "/api/runs/r1/access/"+id2+"?wait=5", tok, ""); m["status"] != "rejected" || m["note"] != "no" {
		t.Fatalf("reject: %v", m)
	}
	// cancel: pending -> rejected/expired; cancelling a decided one keeps its status
	_, m = e.create(tok, "r1", accBody)
	id3 := apID(m)
	if st, m = e.do("POST", "/api/runs/r1/access/"+id3+"/cancel", tok, ""); st != 200 || m["status"] != "rejected" {
		t.Fatalf("cancel: %d %v", st, m)
	}
	if got, _ := e.ap.Get(id3); got.Note != "expired" {
		t.Fatalf("note: %q", got.Note)
	}
	if _, m = e.do("POST", "/api/runs/r1/access/"+id+"/cancel", tok, ""); m["status"] != "approved" {
		t.Fatalf("cancel decided: %v", m)
	}
	// audit: actor agent
	es, _ := e.au.Read(50)
	found := false
	for _, en := range es {
		if en.Action == "approval.request" && en.Actor == "agent" && en.Target == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("no agent audit entry: %v", es)
	}
}

func TestAccessAuth(t *testing.T) {
	e := newAccessEnv(t)
	t1, t2 := e.newRun("r1"), e.newRun("r2")
	e.expectErr("POST", "/api/runs/r1/access", "", accBody, 401, proto.CodeUnauthorized)
	e.expectErr("POST", "/api/runs/r1/access", "bt_run_nope", accBody, 401, proto.CodeUnauthorized)
	e.expectErr("POST", "/api/runs/r1/access", t2, accBody, 403, proto.CodeRunMismatch)
	e.expectErr("POST", "/api/runs/r1/access", condTok, accBody, 403, proto.CodeForbidden)
	if st, m := e.create(adminTok, "r1", accBody); st != 200 {
		t.Fatalf("admin create: %d %v", st, m)
	}
	_, m := e.create(t1, "r1", accBody)
	id := apID(m)
	e.expectErr("GET", "/api/runs/r1/access/"+id, t2, "", 403, proto.CodeRunMismatch)
	e.expectErr("GET", "/api/runs/r1/access/"+id, condTok, "", 403, proto.CodeForbidden)
	e.expectErr("POST", "/api/runs/r1/access/"+id+"/cancel", condTok, "", 403, proto.CodeForbidden)
	// an approval of another run is invisible
	e.expectErr("GET", "/api/runs/r2/access/"+id, t2, "", 404, proto.CodeNotFound)
	// the conductor cannot decide approvals
	e.expectErr("POST", "/api/approvals/"+id+"/approve", condTok, "", 401, proto.CodeUnauthorized)
	e.expectErr("POST", "/api/approvals/"+id+"/reject", condTok, "", 401, proto.CodeUnauthorized)
	if got, _ := e.ap.Get(id); got.Status != approvals.Pending {
		t.Fatalf("status %s", got.Status)
	}
}

func TestAccessValidationAndRedaction(t *testing.T) {
	e := newAccessEnv(t)
	tok := e.newRun("r1")
	e.expectErr("POST", "/api/runs/r1/access", tok, `{"kind":"bogus","summary":"x"}`, 400, proto.CodeInvalidPayload)
	e.expectErr("POST", "/api/runs/r1/access", tok, `{"kind":"command","summary":""}`, 400, proto.CodeInvalidPayload)
	e.expectErr("POST", "/api/runs/r1/access", tok, `{"kind":"command","summary":"x","detail":[1]}`, 400, proto.CodeInvalidPayload)
	secret := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	_, m := e.create(tok, "r1", fmt.Sprintf(`{"kind":"command","summary":"run with api_key=%s","detail":{"command":"x --token=%s"}}`, secret, secret))
	b := mustJSON(m)
	if strings.Contains(b, secret) {
		t.Fatalf("secret leaked: %s", b)
	}
	_, m = e.create(tok, "r1", `{"kind":"command","summary":"`+strings.Repeat("é", 500)+`"}`)
	ap := m["approval"].(map[string]any)
	if n := len([]rune(ap["detail"].(map[string]any)["summary"].(string))); n != 300 {
		t.Fatalf("summary len %d", n)
	}
}

func TestAccessRunEnded(t *testing.T) {
	e := newAccessEnv(t)
	tok := e.newRun("r1")
	_, m := e.create(tok, "r1", accBody)
	id := apID(m)
	if _, err := e.st.Get("r1").AppendHub("exit", map[string]any{"code": 0, "signal": nil, "diff_stat": nil, "duration_s": 1}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { got, _ := e.ap.Get(id); return got.Status == approvals.Rejected })
	if got, _ := e.ap.Get(id); got.Note != "run ended" {
		t.Fatalf("note %q", got.Note)
	}
	// the driver can still read the answer with the (revoked) run token, and creating gives 410
	if _, m = e.do("GET", "/api/runs/r1/access/"+id, tok, ""); m["status"] != "rejected" || m["note"] != "run ended" {
		t.Fatalf("poll: %v", m)
	}
	e.expectErr("POST", "/api/runs/r1/access", tok, accBody, 410, proto.CodeRunEnded)
	e.expectErr("POST", "/api/runs/r1/access", adminTok, accBody, 410, proto.CodeRunEnded)
}
