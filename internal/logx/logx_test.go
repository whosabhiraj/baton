package logx

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotateKeepsThree(t *testing.T) {
	dir := t.TempDir()
	lg, c, err := open(dir, slog.LevelInfo, 500)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		lg.Info("line", "i", i, "pad", strings.Repeat("x", 50))
	}
	c.Close()
	for _, n := range []string{"hub.log", "hub.log.1", "hub.log.2", "hub.log.3"} {
		if _, err := os.Stat(filepath.Join(dir, "logs", n)); err != nil {
			t.Errorf("missing %s", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "hub.log.4")); err == nil {
		t.Error("hub.log.4 must not exist")
	}
}

func TestRedactsAndJSON(t *testing.T) {
	dir := t.TempDir()
	lg, c, err := Open(dir, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	lg.Info("tok bt_adm_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ", "k", "bt_run_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ")
	lg.Debug("hidden")
	c.Close()
	b, _ := os.ReadFile(filepath.Join(dir, "logs", "hub.log"))
	s := string(b)
	if !strings.HasPrefix(s, "{") || strings.Contains(s, "abcdefghijklmnop") || strings.Contains(s, "hidden") {
		t.Fatalf("bad log: %s", s)
	}
}
