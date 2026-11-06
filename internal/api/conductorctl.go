package api

// Phase 6b (A17): the conductor process controls and its history, for the terminal chat and the
// dashboard. Admin token only (the conductor never drives itself through these).

import (
	"net/http"
	"strconv"

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
