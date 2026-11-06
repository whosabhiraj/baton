package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whosabhiraj/baton/internal/hub"
)

func TestInfoRoundTripAndStale(t *testing.T) {
	dd := t.TempDir()
	in := Info{PID: 1, Port: 2, URL: "http://127.0.0.1:1", Instance: "x", Version: "v", DataDir: dd}
	if err := WriteInfo(dd, in); err != nil {
		t.Fatal(err)
	}
	got, err := ReadInfo(dd)
	if err != nil || *got != in {
		t.Fatalf("%v %+v", err, got)
	}
	// stale: nothing listens, lock free -> Stopped and hub.json removed
	_, st, err := Discover(dd)
	if err != nil || st != Stopped {
		t.Fatal(st, err)
	}
	if _, err := os.Stat(filepath.Join(dd, InfoFile)); !os.IsNotExist(err) {
		t.Fatal("stale hub.json not removed")
	}
}

func TestDiscoverRunningStartingStop(t *testing.T) {
	dd := t.TempDir()
	h, err := hub.Start(context.Background(), hub.Options{DataDir: dd})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())

	// lock held, no hub.json -> Starting
	if _, st, _ := Discover(dd); st != Starting {
		t.Fatalf("want Starting, got %v", st)
	}
	// wrong instance -> not Running
	_ = WriteInfo(dd, Info{PID: os.Getpid(), URL: h.URL, Instance: "other"})
	if _, st, _ := Discover(dd); st != Starting {
		t.Fatalf("instance mismatch: got %v", st)
	}
	// real instance from /api/health
	inst := healthInstance(t, h.URL)
	_ = WriteInfo(dd, Info{PID: os.Getpid(), URL: h.URL, Instance: inst})
	in, st, _ := Discover(dd)
	if st != Running || in.URL != h.URL {
		t.Fatalf("want Running, got %v", st)
	}
	if err := Stop(dd, "wrong", time.Second); err == nil {
		t.Fatal("wrong token must fail")
	}
	if err := Stop(dd, h.AdminToken, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, st, _ := Discover(dd); st != Stopped {
		t.Fatalf("after stop: %v", st)
	}
	if err := Stop(dd, "x", time.Second); err != nil {
		t.Fatal("stopping a stopped hub is fine:", err)
	}
}

func healthInstance(t *testing.T, url string) string {
	t.Helper()
	resp, err := client.Get(url + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m struct{ Instance string }
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	s := string(buf[:n])
	i := indexOf(s, `"instance":"`)
	if i < 0 {
		t.Fatal(s)
	}
	s = s[i+len(`"instance":"`):]
	m.Instance = s[:indexOf(s, `"`)]
	return m.Instance
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
