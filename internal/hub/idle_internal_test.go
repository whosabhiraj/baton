package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestInflightRequestKeepsHubAwake: an admin request that runs longer than the idle delay (a
// spawn creating its worktree on a slow disk) keeps the hub awake until it ends.
func TestInflightRequestKeepsHubAwake(t *testing.T) {
	d := t.TempDir()
	gp := filepath.Join(d, "config.yaml")
	if err := os.WriteFile(gp, []byte("hub:\n  mode: auto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := Start(context.Background(), Options{DataDir: filepath.Join(d, "data"), AdminToken: "tok",
		GlobalConfig: gp, ProjectConfig: filepath.Join(d, "none.yaml"), ConfigPoll: 30 * time.Millisecond,
		SpoolPoll: 30 * time.Millisecond, IdleStop: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(context.Background()) })
	end := h.apiTouch() // a request starts and is still running
	select {
	case <-h.Done():
		t.Fatal("hub stopped while a request was running")
	case <-time.After(1200 * time.Millisecond):
	}
	end()
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("idle hub did not stop after the request ended")
	}
}
