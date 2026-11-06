package approvals

import (
	"errors"
	"testing"
)

func TestRequestDecideReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Add(KindMerge, "r1", "Merge x", map[string]any{"branch": "b"}, "conductor")
	if err != nil || a.Status != Pending || a.ID[:3] != "ap_" {
		t.Fatal(a, err)
	}
	b, _ := s.Add(KindSpawn, "", "Spawn y", nil, "conductor")
	if _, err := s.Decide(a.ID, true, "human", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(a.ID, false, "human", ""); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatal(err)
	}
	if _, err := s.Decide("ap_nope", true, "human", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	s2, err := Open(dir) // persisted
	if err != nil {
		t.Fatal(err)
	}
	all, pend := s2.List(""), s2.List(Pending)
	if len(all) != 2 || all[0].Status != Approved || all[0].DecidedBy != "human" || all[0].Note != "ok" ||
		len(pend) != 1 || pend[0].ID != b.ID {
		t.Fatal(all, pend)
	}
}
