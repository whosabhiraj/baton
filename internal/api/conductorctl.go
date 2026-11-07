package api

// Phase 6b (A17): the conductor process controls and its history, for the terminal chat and the
// dashboard. Admin token only (the conductor never drives itself through these).

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/whosabhiraj/baton/internal/conductor"
	"github.com/whosabhiraj/baton/internal/proto"
)

// ConductorCtl is the hub's conductor (internal/conductor.Manager).
type ConductorCtl interface {
	Info() conductor.Info
	Start() error
	Stop()
	Reset()
	Send(text, from string) error
	History(after, limit int) ([]conductor.Entry, int, error)
	Subscribe() (<-chan conductor.Update, func())
	Seen(cursor uint64)               // the conductor read the bus up to cursor (GET /api/wait)
	SeenCursor() uint64               // where the conductor last read up to (0 = nothing yet)
	Usage() map[string]map[string]any // the conductor's own usage totals per account
}

func (a *api) noConductor(w http.ResponseWriter) bool {
	if a.ConductorCtl == nil {
		writeErr(w, proto.NewError(proto.CodeNoConductor, "this hub has no conductor (it needs a git repository)"))
		return true
	}
	return false
}

func (a *api) conductorInfo(w http.ResponseWriter, r *http.Request) {
	if a.noConductor(w) {
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "conductor": a.ConductorCtl.Info()})
}

func (a *api) conductorStart(w http.ResponseWriter, r *http.Request) {
	if a.noConductor(w) {
		return
	}
	if err := a.ConductorCtl.Start(); err != nil {
		writeErr(w, proto.NewError(proto.CodeNoConductor, err.Error()))
		return
	}
	a.recordReq(r, "conductor.start", "", nil)
	a.conductorInfo(w, r)
}

func (a *api) conductorStop(w http.ResponseWriter, r *http.Request) {
	if a.noConductor(w) {
		return
	}
	a.ConductorCtl.Stop()
	a.recordReq(r, "conductor.stop", "", nil)
	a.conductorInfo(w, r)
}

func (a *api) conductorReset(w http.ResponseWriter, r *http.Request) {
	if a.noConductor(w) {
		return
	}
	a.ConductorCtl.Reset()
	a.recordReq(r, "conductor.reset", "", nil)
	a.conductorInfo(w, r)
}

func (a *api) conductorSend(w http.ResponseWriter, r *http.Request) {
	if a.noConductor(w) {
		return
	}
	body, perr := readObject(r)
	if perr != nil {
		writeErr(w, perr)
		return
	}
	text, _ := body["text"].(string)
	if text == "" {
		writeErr(w, proto.NewError(proto.CodeInvalidPayload, "text is required"))
		return
	}
	if err := a.ConductorCtl.Send(text, "human"); err != nil {
		writeErr(w, proto.NewError(proto.CodeNoConductor, err.Error()))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "conductor": a.ConductorCtl.Info()})
}

func (a *api) conductorHistory(w http.ResponseWriter, r *http.Request) {
	if a.noConductor(w) {
		return
	}
	q := r.URL.Query()
	after, limit := 0, 0
	for k, p := range map[string]*int{"after": &after, "limit": &limit} {
		if v := q.Get(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad "+k))
				return
			}
			*p = n
		}
	}
	es, last, err := a.ConductorCtl.History(after, limit)
	if err != nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "cannot read conductor history"))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "entries": es, "last": last})
}

// conductorStream (GET /api/conductor/stream, SSE; admin via header or ?token=) sends
// `event: entry` (id = entry n) for history entries after Last-Event-ID / ?after (absent = the
// last 200), then live entries and `event: state` changes.
func (a *api) conductorStream(w http.ResponseWriter, r *http.Request) {
	if a.noConductor(w) {
		return
	}
	after, replay := 0, 200
	lid := r.Header.Get("Last-Event-ID")
	if lid == "" {
		lid = r.URL.Query().Get("after")
	}
	if lid != "" {
		n, err := strconv.Atoi(lid)
		if err != nil || n < 0 {
			writeErr(w, proto.NewError(proto.CodeInvalidPayload, "bad after"))
			return
		}
		after, replay = n, 5000
	}
	ch, cancel := a.ConductorCtl.Subscribe() // before reading history: nothing falls in between
	defer cancel()
	past, _, err := a.ConductorCtl.History(after, replay)
	if err != nil {
		writeErr(w, proto.NewError(proto.CodeInternal, "cannot read conductor history"))
		return
	}
	rc := http.NewResponseController(w)
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
	data := func(v any) string {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		return "data: " + string(bytes.TrimRight(buf.Bytes(), "\n")) + "\n\n"
	}
	info := a.ConductorCtl.Info()
	if !write("retry: 2000\n\nevent: state\n" + data(info.State)) {
		return
	}
	sent := after
	for _, e := range past {
		if !write("id: " + strconv.Itoa(e.N) + "\nevent: entry\n" + data(e)) {
			return
		}
		sent = e.N
	}
	tick := time.NewTicker(a.KeepAlive)
	defer tick.Stop()
	for {
		select {
		case u, ok := <-ch:
			if !ok {
				return // fell behind: the client reconnects with Last-Event-ID
			}
			switch {
			case u.Entry != nil && u.Entry.N > sent:
				if !write("id: " + strconv.Itoa(u.Entry.N) + "\nevent: entry\n" + data(u.Entry)) {
					return
				}
				sent = u.Entry.N
			case u.State != nil:
				if !write("event: state\n" + data(u.State)) {
					return
				}
			}
		case <-tick.C:
			if !write(": ping\n\n") {
				return
			}
		case <-r.Context().Done():
			return
		case <-a.Done:
			return
		}
	}
}
