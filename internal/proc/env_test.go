package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func toMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		m[norm(kv[:i])] = kv[i+1:]
	}
	return m
}

func TestBuildEnvAllowlist(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	t.Setenv("BATON_ADMIN_TOKEN", "adm")
	t.Setenv("LC_ALL", "C")
	t.Setenv("MY_PASS", "p")
	t.Setenv("PATH", "/x")
	env := BuildEnv([]string{"MY_PASS"}, nil, nil)
	m := toMap(env)
	for _, k := range []string{"ANTHROPIC_API_KEY", "BATON_ADMIN_TOKEN"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s leaked", k)
		}
	}
	if m["PATH"] != "/x" || m["LC_ALL"] != "C" || m["MY_PASS"] != "p" {
		t.Errorf("missing allowed vars: %v", m)
	}
}

func TestBuildEnvLayers(t *testing.T) {
	t.Setenv("LANG", "en")
	home, _ := os.UserHomeDir()
	env := BuildEnv(nil,
		map[string]string{"LANG": "fr", "CFG": "~/.cfg", "BARE": "~", "NOT": "a~b"},
		map[string]string{"BATON_TOKEN": "t", "CFG2": "z"})
	m := toMap(env)
	if m["LANG"] != "fr" {
		t.Errorf("account should override: %q", m["LANG"])
	}
	if m["CFG"] != filepath.Join(home, ".cfg") || m["BARE"] != home || m["NOT"] != "a~b" {
		t.Errorf("tilde: %q %q %q", m["CFG"], m["BARE"], m["NOT"])
	}
	if m["BATON_TOKEN"] != "t" {
		t.Error("baton env missing")
	}
	if len(env) != len(m) {
		t.Errorf("duplicate keys: %v", env)
	}
}

func TestBuildEnvWindowsCaseDedup(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	m := toMap(BuildEnv(nil, map[string]string{"Path": "C:-only"}, nil))
	if m["PATH"] != "C:-only" {
		t.Errorf("got %q", m["PATH"])
	}
	n := 0
	for _, kv := range BuildEnv(nil, map[string]string{"Path": "C:-only"}, nil) {
		if strings.EqualFold(strings.SplitN(kv, "=", 2)[0], "path") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want one PATH, got %d", n)
	}
}

func TestPrependPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := PrependPath([]string{"A=1", "PATH=old"}, "/d")
	if got[1] != "PATH=/d"+sep+"old" || got[0] != "A=1" {
		t.Errorf("%v", got)
	}
	if got := PrependPath([]string{"A=1"}, "/d"); got[1] != "PATH=/d" {
		t.Errorf("%v", got)
	}
	if runtime.GOOS == "windows" {
		if got := PrependPath([]string{"Path=old"}, `C:\d`); got[0] != `Path=C:\d;old` || len(got) != 1 {
			t.Errorf("%v", got)
		}
	}
}
