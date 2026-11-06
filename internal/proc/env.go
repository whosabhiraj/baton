// Package proc builds the environment agents run in (GO_HUB_DESIGN §6.2): an allowlist of the
// hub's own environment, never a wholesale inherit, so stray secrets cannot leak into agents.
package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var allow = []string{
	"PATH", "HOME", "USERPROFILE", "SystemRoot", "SystemDrive", "ComSpec", "PATHEXT", "TEMP", "TMP",
	"APPDATA", "LOCALAPPDATA", "ProgramData", "ProgramFiles", "ProgramFiles(x86)", "LANG", "TERM", "TZ",
}

func norm(k string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(k)
	}
	return k
}

// envSet is an ordered environment map; keys compare case-insensitively on Windows.
type envSet struct {
	keys []string
	vals map[string]string
	idx  map[string]int
}

func newEnvSet() *envSet { return &envSet{vals: map[string]string{}, idx: map[string]int{}} }

func (e *envSet) set(k, v string) {
	n := norm(k)
	if i, ok := e.idx[n]; ok {
		e.keys[i] = k
	} else {
		e.idx[n] = len(e.keys)
		e.keys = append(e.keys, k)
	}
	e.vals[n] = v
}

func (e *envSet) list() []string {
	out := make([]string, 0, len(e.keys))
	for _, k := range e.keys {
		out = append(out, k+"="+e.vals[norm(k)])
	}
	return out
}

// BuildEnv returns KEY=VALUE entries: allowlisted variables of the current process (plus the
// passthrough names), then the account env (leading ~ expanded to the home dir), then the baton
// env (BATON_*). Later layers override earlier ones. Nothing else from the process env is kept.
func BuildEnv(passthrough []string, account map[string]string, baton map[string]string) []string {
	ok := map[string]bool{}
	for _, k := range allow {
		ok[norm(k)] = true
	}
	for _, k := range passthrough {
		ok[norm(k)] = true
	}
	e := newEnvSet()
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 { // also skips Windows "=C:" entries
			continue
		}
		k := kv[:i]
		if ok[norm(k)] || strings.HasPrefix(norm(k), "LC_") {
			e.set(k, kv[i+1:])
		}
	}
	home, _ := os.UserHomeDir()
	for k, v := range account {
		e.set(k, expandTilde(v, home))
	}
	for k, v := range baton {
		e.set(k, v)
	}
	return e.list()
}

func expandTilde(v, home string) string {
	if home == "" {
		return v
	}
	if v == "~" {
		return home
	}
	if strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
		return filepath.Join(home, v[2:])
	}
	return v
}

// PrependPath puts dir at the front of PATH in env (key matched case-insensitively on Windows),
// adding PATH if absent. It returns a new slice.
func PrependPath(env []string, dir string) []string {
	out := append([]string(nil), env...)
	sep := string(os.PathListSeparator)
	for i, kv := range out {
		j := strings.IndexByte(kv, '=')
		if j > 0 && norm(kv[:j]) == "PATH" {
			if old := kv[j+1:]; old != "" {
				out[i] = kv[:j+1] + dir + sep + old
			} else {
				out[i] = kv[:j+1] + dir
			}
			return out
		}
	}
	return append(out, "PATH="+dir)
}
