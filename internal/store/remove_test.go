package store_test

import (
	"os"
	"testing"
)

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	r := newRun(t, s)
	if _, _, e := r.IngestEnvelope(hb("h1"), "cli"); e != nil { // opens the event log
		t.Fatal(e)
	}
	rd := r.Dir()
	if err := s.Remove("r_TEST"); err != nil {
		t.Fatal(err)
	}
	if s.Get("r_TEST") != nil || len(s.List()) != 0 {
		t.Fatal("run still indexed")
	}
	if _, err := os.Stat(rd); err == nil {
		t.Fatal("run folder remains")
	}
	if err := s.Remove("r_TEST"); err == nil {
		t.Fatal("want error for unknown run")
	}
	// a fresh store does not see it either
	if s2 := openStore(t, dir); len(s2.List()) != 0 {
		t.Fatal("run reloaded")
	}
}
