// Package api is the hub's HTTP surface (PROTOCOL.md A11/A12): routes, auth classes, JSON
// errors, the SSE stream, the Host-header guard, the body limit and panic recovery.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/whosabhiraj/baton/internal/approvals"
	"github.com/whosabhiraj/baton/internal/audit"
	"github.com/whosabhiraj/baton/internal/bus"
	"github.com/whosabhiraj/baton/internal/config"
	"github.com/whosabhiraj/baton/internal/proto"
	"github.com/whosabhiraj/baton/internal/runs"
	"github.com/whosabhiraj/baton/internal/store"
)

// Deps are the collaborators of the API.
type Deps struct {
	Store      *store.Store
	Bus        *bus.Broker
	AdminToken string
	Instance   string // random id of this hub process (A12)
	Dev        bool   // enables POST /api/dev/runs
	// Port is the listener port; when non-zero the Host header must carry exactly this port.
	Port int
	// URL is the hub base URL put into agent env; empty = derived from the request Host.
	URL string
	// Runs spawns agents (nil = POST /api/spawn answers 409 NOT_A_REPO). Audit and Config may be
	// nil (tests): audit entries are then dropped and the config routes answer 404.
	Runs   *runs.Manager
	Audit  *audit.Log
	Config *config.Watcher
	// Approvals, Conductor, OnApproval and Launcher are the Phase 3 hooks (A15); all optional.
	Approvals  *approvals.Store
	Conductor  func() proto.ConductorState
	OnApproval func(approvals.Approval)
	Launcher   runs.Launcher
	// CheckConductor reports whether tok is the current conductor session token (A16). nil =
	// no conductor tokens are accepted.
	CheckConductor func(tok string) bool
	// ConductorCtl runs the headless conductor (A17); nil = /api/conductor/* answer NO_CONDUCTOR.
	ConductorCtl ConductorCtl
	// FakeMerges marks runs merged without touching git (baton mock).
	FakeMerges bool
	// Touch, if set, is called for every authenticated admin request (idle-stop bookkeeping).
	Touch func()
	// Shutdown is called (asynchronously, after the response) by POST /api/hub/shutdown.
	Shutdown func()
	// Done is closed when the hub shuts down; it ends SSE streams.
	Done   <-chan struct{}
	Logger *slog.Logger
	// MaxBody is the request body limit (default 1 MiB).
	MaxBody int64
	// KeepAlive is the SSE comment interval (default 15 s); WriteTimeout the per-write
	// deadline on SSE streams (default 10 s).
	KeepAlive    time.Duration
	WriteTimeout time.Duration
	// Dashboard serves non-API paths (nil = a one-line "not built" page).
	Dashboard http.Handler
}

type api struct {
	Deps
	adminHash [32]byte
}

// New builds the hub's http.Handler.
func New(d Deps) http.Handler {
	if d.MaxBody <= 0 {
		d.MaxBody = 1 << 20
	}
	if d.KeepAlive <= 0 {
		d.KeepAlive = 15 * time.Second
	}
	if d.WriteTimeout <= 0 {
		d.WriteTimeout = 10 * time.Second
	}
	if d.Dashboard == nil {
		d.Dashboard = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "Baton hub is running. Dashboard not built - run `npm run build`.\n")
		})
	}
	a := &api{Deps: d, adminHash: sha256.Sum256([]byte(d.AdminToken))}
	mux := http.NewServeMux()
	// authAdminOrCond routes are the conductor's tools (A16); the rest stay admin-only.
	mux.HandleFunc("/api/health", a.route("GET", authNone, a.health))
	mux.HandleFunc("/api/ingest", a.route("POST", authRun, a.ingest))
	mux.HandleFunc("/api/runs/{id}/inbox", a.route("GET", authRunOrAdmin, a.inbox))
	mux.HandleFunc("/api/runs/{id}/events", a.route("GET", authRunOrAdmin, a.events))
	mux.HandleFunc("/api/state", a.route("GET", authAdminOrCond, a.state))
	mux.HandleFunc("/api/stream", a.route("GET", authAdminQuery, a.stream))
	for _, t := range []string{"message", "answer", "plan_update", "pause", "resume", "stop"} {
		mux.HandleFunc("/api/runs/{id}/"+t, a.route("POST", authAdminOrCond, a.deliver(t)))
	}
	mux.HandleFunc("/api/runs", a.route("GET", authAdminOrCond, a.listRuns))
	mux.HandleFunc("/api/runs/{id}", a.route("GET", authAdminOrCond, a.getRun))
	mux.HandleFunc("/api/runs/{id}/diff", a.route("GET", authAdminOrCond, a.diff))
	mux.HandleFunc("/api/runs/{id}/merge", a.route("POST", authAdminOrCond, a.merge))
	mux.HandleFunc("/api/wait", a.route("GET", authAdminOrCond, a.wait))
	mux.HandleFunc("/api/spawn", a.route("POST", authAdminOrCond, a.spawn))
	mux.HandleFunc("/api/accounts", a.route("GET", authAdminOrCond, a.listAccounts))
	mux.HandleFunc("/api/accounts/{id}", a.route("PATCH", authAdminOrCond, a.patchAccount))
	mux.HandleFunc("GET /api/config", a.route("GET", authAdminOrCond, a.getConfig))
	mux.HandleFunc("PATCH /api/config", a.route("PATCH", authAdminOrCond, a.patchConfig))
	mux.HandleFunc("/api/runs/{id}/kill", a.route("POST", authAdminOrCond, a.kill))
	mux.HandleFunc("/api/approvals", a.route("GET", authAdminOrCond, a.listApprovals))
	mux.HandleFunc("/api/approvals/{id}/approve", a.route("POST", authAdmin, a.decide(true)))
	mux.HandleFunc("/api/approvals/{id}/reject", a.route("POST", authAdmin, a.decide(false)))
	mux.HandleFunc("/api/audit", a.route("GET", authAdminOrCond, a.auditList))
	mux.HandleFunc("/api/conductor", a.route("GET", authAdmin, a.conductorInfo))
	mux.HandleFunc("/api/conductor/start", a.route("POST", authAdmin, a.conductorStart))
	mux.HandleFunc("/api/conductor/stop", a.route("POST", authAdmin, a.conductorStop))
	mux.HandleFunc("/api/conductor/reset", a.route("POST", authAdmin, a.conductorReset))
	mux.HandleFunc("/api/conductor/send", a.route("POST", authAdmin, a.conductorSend))
	mux.HandleFunc("/api/conductor/history", a.route("GET", authAdmin, a.conductorHistory))
	mux.HandleFunc("/api/conductor/stream", a.route("GET", authAdminQuery, a.conductorStream))
	mux.HandleFunc("/api/dev/runs", a.route("POST", authAdmin, a.devRun))
	mux.HandleFunc("/api/hub/shutdown", a.route("POST", authAdmin, a.shutdown))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, proto.NewError(proto.CodeNotFound, "no such route"))
	})
	mux.Handle("/", d.Dashboard)
	return a.recoverMW(a.hostGuard(mux))
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		status = http.StatusInternalServerError
		buf.Reset()
		buf.WriteString(`{"ok":false,"code":"INTERNAL","error":"encode failure"}` + "\n")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func writeErr(w http.ResponseWriter, e *proto.Error) {
	status := e.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]any{"ok": false, "code": e.Code, "error": e.Msg})
}

// ---- middleware ----

func (a *api) recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				if a.Logger != nil {
					a.Logger.Error("handler panic", "path", r.URL.Path, "panic", fmt.Sprint(p))
				}
				writeErr(w, proto.NewError(proto.CodeInternal, "internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// hostGuard refuses requests whose Host is not a loopback name (DNS-rebinding defence).
func (a *api) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.hostAllowed(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "code": "BAD_HOST", "error": "unexpected Host header"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *api) hostAllowed(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil { // no port
		host, port = strings.Trim(hostport, "[]"), ""
		if a.Port != 0 {
			return false
		}
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
	default:
		return false
	}
	return a.Port == 0 || port == strconv.Itoa(a.Port)
}

type authClass int

const (
	authNone authClass = iota
	authRun
	authRunOrAdmin
	authAdmin
	authAdminQuery  // admin via header or ?token= (EventSource)
	authAdminOrCond // admin, or the conductor token (the request's actor is then forced to "conductor")
)

type conductorKey struct{}

// isConductorReq reports whether the request was authenticated with the conductor token.
func isConductorReq(r *http.Request) bool { v, _ := r.Context().Value(conductorKey{}).(bool); return v }

func (a *api) isConductor(tok string) bool {
	return tok != "" && a.CheckConductor != nil && a.CheckConductor(tok)
}

func (a *api) route(method string, class authClass, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "code": "METHOD_NOT_ALLOWED", "error": "use " + method})
			return
		}
		switch class {
		case authAdmin, authAdminQuery:
			tok := bearer(r)
			if tok == "" && class == authAdminQuery {
				tok = r.URL.Query().Get("token")
			}
			if !a.isAdmin(tok) {
				writeErr(w, proto.NewError(proto.CodeUnauthorized, "admin token required"))
				return
			}
			if a.Touch != nil {
				a.Touch()
			}
		case authAdminOrCond:
			tok := bearer(r)
			switch {
			case a.isAdmin(tok):
			case a.isConductor(tok):
				r = r.WithContext(context.WithValue(r.Context(), conductorKey{}, true))
			default:
				writeErr(w, proto.NewError(proto.CodeUnauthorized, "admin or conductor token required"))
				return
			}
			if a.Touch != nil {
				a.Touch()
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, a.MaxBody)
		h(w, r)
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (a *api) isAdmin(tok string) bool {
	if tok == "" {
		return false
	}
	h := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(h[:], a.adminHash[:]) == 1
}

func readBody(r *http.Request) ([]byte, *proto.Error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, &proto.Error{Status: http.StatusRequestEntityTooLarge, Code: "PAYLOAD_TOO_LARGE", Msg: "request body too large"}
		}
		return nil, proto.NewError(proto.CodeInvalidEnvelope, "cannot read body")
	}
	return b, nil
}

// readObject decodes an optional JSON object body (empty = {}); numbers stay exact.
func readObject(r *http.Request) (map[string]any, *proto.Error) {
	b, perr := readBody(r)
	if perr != nil {
		return nil, perr
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil || dec.More() {
		return nil, proto.NewError(proto.CodeInvalidPayload, "body must be a JSON object")
	}
	return m, nil
}

// ---- handlers ----

func (a *api) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "name": "baton", "v": proto.Version, "instance": a.Instance})
}

func (a *api) ingest(w http.ResponseWriter, r *http.Request) {
	run := a.Store.RunByToken(bearer(r))
	if run == nil || (run.TokenRevoked() && !run.Ended()) {
		writeErr(w, proto.NewError(proto.CodeUnauthorized, "valid run token required"))
		return
	}
	body, perr := readBody(r)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	env, perr := proto.ParseIncoming(body)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	if env.RunID != run.ID() {
		writeErr(w, proto.NewError(proto.CodeRunMismatch, "token does not belong to run_id"))
		return
	}
	seq, dup, perr := run.IngestEnvelope(env, proto.SrcCLI)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "seq": seq, "duplicate": dup})
}

// runAccess resolves {id} for run-or-admin routes. It writes the error response itself.
func (a *api) runAccess(w http.ResponseWriter, r *http.Request) *store.Run {
	id := r.PathValue("id")
	tok := bearer(r)
	if tok == "" {
		writeErr(w, proto.NewError(proto.CodeUnauthorized, "token required"))
		return nil
	}
	if a.isAdmin(tok) || a.isConductor(tok) {
		run := a.findRun(id)
		if run == nil {
			writeErr(w, proto.NewError(proto.CodeNotFound, "no such run"))
		}
		return run
	}
	owner := a.Store.RunByToken(tok)
	if owner == nil || owner.TokenRevoked() {
		writeErr(w, proto.NewError(proto.CodeUnauthorized, "valid token required"))
		return nil
	}
	if owner.ID() != id {
		writeErr(w, proto.NewError(proto.CodeRunMismatch, "token belongs to another run"))
		return nil
	}
	return owner
}

func (a *api) inbox(w http.ResponseWriter, r *http.Request) {
	run := a.runAccess(w, r)
	if run == nil {
		return
	}
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad after"))
			return
		}
		after = n
	}
	msgs, err := run.Inbox(after)
	if err != nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "cannot read inbox"))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "messages": msgs})
}

func (a *api) events(w http.ResponseWriter, r *http.Request) {
	run := a.runAccess(w, r)
	if run == nil {
		return
	}
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad since"))
			return
		}
		since = n
	}
	evs, err := run.Events(since)
	if err != nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "cannot read events"))
		return
	}
	// A16: optional types=a,b filter and limit=N (the newest N after filtering).
	if types := splitList(r.URL.Query().Get("types")); len(types) > 0 {
		kept := evs[:0:0]
		for _, ev := range evs {
			if slices.Contains(types, ev.Type) {
				kept = append(kept, ev)
			}
		}
		evs = kept
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad limit"))
			return
		}
		if len(evs) > n {
			evs = evs[len(evs)-n:]
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "events": evs})
}

func (a *api) state(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ok": true, "runs": a.Store.Summaries(), "accounts": a.accounts(), "instance": a.Instance,
		"mode": "", "conductor": a.conductor(), "approvals": a.pendingApprovals(), "config_ok": true, "config_issues": []config.Issue{}}
	if a.Config != nil {
		s := a.Config.Current()
		out["mode"], out["config_ok"], out["config_issues"] = s.Config.Hub.Mode, s.OK(), issuesOf(s.Issues)
	}
	writeJSON(w, 200, out)
}

func (a *api) deliver(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		run := a.findRun(r.PathValue("id"))
		if run == nil {
			writeErr(w, proto.NewError(proto.CodeNotFound, "no such run"))
			return
		}
		body, perr := readObject(r)
		if perr != nil {
			writeErr(w, perr)
			return
		}
		if typ == "message" {
			if _, ok := body["from"]; !ok || isConductorReq(r) {
				body["from"] = "human"
				if isConductorReq(r) {
					body["from"] = "conductor"
				}
			}
		}
		n, perr := run.Deliver(typ, body)
		if perr != nil {
			writeErr(w, perr)
			return
		}
		a.recordReq(r, "deliver."+typ, run.ID(), map[string]any{"n": n})
		writeJSON(w, 200, map[string]any{"ok": true, "n": n})
	}
}

func (a *api) shutdown(w http.ResponseWriter, r *http.Request) {
	a.recordReq(r, "hub.shutdown", "", nil)
	writeJSON(w, 200, map[string]any{"ok": true})
	if a.Shutdown != nil {
		go a.Shutdown()
	}
}

func (a *api) devRun(w http.ResponseWriter, r *http.Request) {
	if !a.Dev {
		writeErr(w, proto.NewError(proto.CodeNotFound, "dev routes are disabled"))
		return
	}
	body, perr := readObject(r)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	str := func(k string) string { s, _ := body[k].(string); return s }
	c := store.CreateOpts{
		AgentID: str("agent_id"), Callsign: str("callsign"), Role: str("role"), Kind: str("kind"),
		Model: str("model"), AccountID: str("account_id"),
	}
	if c.AgentID == "" {
		c.AgentID = fmt.Sprintf("gn-dev-%04d", len(a.Store.List())+1)
	}
	if raw, ok := body["plan"]; ok {
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &c.Plan); err != nil {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "plan must be an array of {id,title,acceptance}"))
			return
		}
		for _, s := range c.Plan {
			if s.ID == "" || s.Title == "" || s.Acceptance == "" {
				writeErr(w, proto.NewError(proto.CodeInvalidPayload, "every plan step needs id, title and acceptance"))
				return
			}
		}
	}
	run, tok, err := a.Store.Create(c)
	if err != nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "cannot create run"))
		return
	}
	brief := filepath.Join(run.Dir(), "BATON_BRIEF.md")
	_ = store.WriteFileAtomic(brief, []byte(placeholderBrief(run.ID(), c)), 0o644)
	base := a.URL
	if base == "" {
		base = "http://" + r.Host
	}
	env := map[string]string{
		"BATON_URL": base, "BATON_TOKEN": tok, "BATON_RUN_ID": run.ID(), "BATON_AGENT_ID": run.AgentID(),
		"BATON_RUN_DIR": run.Dir(), "BATON_BRIEF": brief,
	}
	a.recordReq(r, "dev.run", run.ID(), map[string]any{"agent_id": run.AgentID(), "role": c.Role, "account_id": c.AccountID})
	writeJSON(w, 200, map[string]any{
		"ok": true, "run_id": run.ID(), "agent_id": run.AgentID(), "token": tok, "run_dir": run.Dir(), "env": env,
	})
}

func placeholderBrief(runID string, c store.CreateOpts) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Baton brief (dev placeholder)\n\nRun `%s`, agent `%s`.\n\n## Plan\n\n", runID, c.AgentID)
	if len(c.Plan) == 0 {
		sb.WriteString("(no plan)\n")
	}
	for _, s := range c.Plan {
		fmt.Fprintf(&sb, "- **%s** %s - acceptance: %s\n", s.ID, s.Title, s.Acceptance)
	}
	return sb.String()
}

// ---- SSE ----

func (a *api) stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	var lastID uint64
	hasLast := false
	lid := r.Header.Get("Last-Event-ID")
	if lid == "" {
		lid = r.URL.Query().Get("lastEventId")
	}
	if lid != "" {
		if n, err := strconv.ParseUint(lid, 10, 64); err == nil {
			lastID, hasLast = n, true
		}
	}
	sub, replay, reset := a.Bus.Subscribe(lastID, hasLast)
	defer sub.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(s string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(a.WriteTimeout))
		if _, err := io.WriteString(w, s); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	hello, _ := json.Marshal(map[string]string{"instance": a.Instance})
	if !write("retry: 2000\n\nevent: hello\ndata: " + string(hello) + "\n\n") {
		return
	}
	resetFrame := "event: reset\ndata: " + string(hello) + "\n\n"
	if reset && !write(resetFrame) {
		return
	}
	send := func(it bus.Item) bool {
		if it.Notice != nil {
			m := map[string]any{}
			for k, v := range it.Notice.Data {
				m[k] = v
			}
			m["type"] = it.Notice.Type
			b, err := json.Marshal(m)
			if err != nil {
				return true
			}
			return write("id: " + strconv.FormatUint(it.N, 10) + "\nevent: hub\ndata: " + string(b) + "\n\n")
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(it.Ev)
		return write("id: " + strconv.FormatUint(it.N, 10) + "\nevent: baton\ndata: " + strings.TrimRight(buf.String(), "\n") + "\n\n")
	}
	for _, it := range replay {
		if !send(it) {
			return
		}
	}
	tick := time.NewTicker(a.KeepAlive)
	defer tick.Stop()
	for {
		select {
		case it := <-sub.C:
			if !send(it) {
				return
			}
		case <-sub.Dropped:
			write(resetFrame)
			return
		case <-tick.C:
			if !write(": ping\n\n") {
				return
			}
		case <-r.Context().Done():
			return
		case <-a.Done:
			return
		case <-a.Bus.Closed:
			return
		}
	}
}
