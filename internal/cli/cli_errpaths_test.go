package cli

import (
	"regexp"
	"testing"
)

// A9: exit 0 for everything except usage errors (2); a rejected or unreachable hub never fails the agent.
func TestErrPathsExitCodesByHubAnswer(t *testing.T) {
	f := newFakeHub(t)
	cases := []struct {
		name    string
		st      int
		reply   string
		code    int
		errRe   string
		spooled bool
	}{
		{"404", 404, `{"ok":false,"code":"NOT_FOUND","error":"no run"}`, 0, `^baton: rejected: NOT_FOUND$`, false},
		{"409", 409, `{"ok":false,"code":"ACCOUNT_BUSY","error":"x"}`, 0, `^baton: rejected: ACCOUNT_BUSY$`, false},
		{"413", 413, ``, 0, `^baton: rejected: 413$`, false},
		{"403 run mismatch", 403, `{"ok":false,"code":"RUN_MISMATCH","error":"x"}`, 0, `^baton: rejected: RUN_MISMATCH$`, false},
		{"403 agent mismatch", 403, `{"ok":false,"code":"AGENT_MISMATCH","error":"x"}`, 0, `^baton: rejected: AGENT_MISMATCH$`, false},
		{"400 hub only type", 400, `{"ok":false,"code":"HUB_ONLY_TYPE","error":"exit is hub-only"}`, 2, `^baton: rejected: HUB_ONLY_TYPE exit is hub-only$`, false},
		{"503", 503, ``, 0, ``, true},
		{"502 html", 502, `<html>bad gateway</html>`, 0, ``, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.set(c.st, c.reply)
			r := newRun(t, "BATON_URL", f.URL)
			code, out, e := r.do("", "heartbeat")
			if code != c.code {
				t.Fatalf("code %d out %q err %q", code, out, e)
			}
			if c.errRe != "" && !regexp.MustCompile(c.errRe).MatchString(e) {
				t.Fatalf("err %q", e)
			}
			if c.spooled && out != "spooled (hub unreachable)" {
				t.Fatalf("out %q", out)
			}
			if (len(r.outbox()) == 1) != c.spooled {
				t.Fatalf("spooled=%d", len(r.outbox()))
			}
		})
	}
}

func TestErrPathsHubDownSpoolsAndExitsZero(t *testing.T) {
	f := newFakeHub(t)
	url := f.URL
	f.Close() // connection refused from here on
	for _, args := range [][]string{
		{"hello"}, {"progress", "5", "x"}, {"step", "s1", "start"}, {"error", "boom", "--fatal"},
		{"done", "--status", "success", "--summary", "ok"}, {"ask", "q?"}, {"artifact", "file", "x"},
	} {
		r := newRun(t, "BATON_URL", url)
		code, out, e := r.do("", args...)
		if code != 0 || out == "" || e != "" {
			t.Errorf("%v: code %d out %q err %q", args, code, out, e)
		}
		if len(r.outbox()) != 1 {
			t.Errorf("%v: not spooled", args)
		}
	}
	// without a url or token the spool is the only path, still exit 0
	r := newRun(t, "BATON_URL", "", "BATON_TOKEN", "")
	if code, out, _ := r.do("", "heartbeat"); code != 0 || out != "spooled (hub unreachable)" || len(r.outbox()) != 1 {
		t.Fatalf("no url: %d %q", code, out)
	}
}
