package audit

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestAppendRead(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 5; i++ {
		if err := l.Append(Entry{Actor: ActorHuman, Action: "x", Target: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := l.Read(0)
	if len(all) != 5 || all[0].Target != "0" || all[4].Target != "4" || all[0].TS == "" {
		t.Fatalf("%+v", all)
	}
	last, _ := l.Read(2)
	if len(last) != 2 || last[0].Target != "3" || last[1].Target != "4" {
		t.Fatalf("%+v", last)
	}
	// reopen appends
	l.Close()
	l2, _ := Open(dir)
	defer l2.Close()
	_ = l2.Append(Entry{Actor: ActorHub, Action: "y"})
	all, _ = l2.Read(0)
	if len(all) != 6 {
		t.Fatalf("len %d", len(all))
	}
	if err := l.Append(Entry{}); err == nil {
		t.Fatal("append after close should fail")
	}
}

func TestRedaction(t *testing.T) {
	l, _ := Open(t.TempDir())
	defer l.Close()
	secret := "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	_ = l.Append(Entry{Actor: ActorHuman, Action: "cfg", Detail: map[string]any{"token": secret, "note": "key " + secret, "n": 3}})
	b, _ := os.ReadFile(l.Path())
	if strings.Contains(string(b), secret) {
		t.Fatalf("secret leaked: %s", b)
	}
	es, _ := l.Read(1)
	if es[0].Detail["n"] == nil {
		t.Fatalf("detail lost: %+v", es[0])
	}
}

func TestConcurrentOneLineEach(t *testing.T) {
	l, _ := Open(t.TempDir())
	defer l.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = l.Append(Entry{Actor: ActorHub, Action: "c", Detail: map[string]any{"i": i, "pad": strings.Repeat("z", 2000)}})
		}(i)
	}
	wg.Wait()
	es, err := l.Read(0)
	if err != nil || len(es) != 50 {
		t.Fatalf("len %d err %v", len(es), err)
	}
}
