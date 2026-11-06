// Package logx opens the hub's log: JSON lines in <dataDir>/logs/hub.log, rotated by size
// (10 MiB, 3 old files kept as hub.log.1..3), behind the redacting handler (design §5.7).
package logx

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/whosabhiraj/baton/internal/redact"
)

const (
	// MaxSize is the size at which hub.log is rotated.
	MaxSize = 10 << 20
	// Keep is how many rotated files are kept.
	Keep = 3
	// FileName is the active log file in <dataDir>/logs.
	FileName = "hub.log"
)

// Open creates the logger. The Closer closes the underlying file.
func Open(dataDir string, level slog.Level) (*slog.Logger, io.Closer, error) {
	return open(dataDir, level, MaxSize)
}

func open(dataDir string, level slog.Level, max int64) (*slog.Logger, io.Closer, error) {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	w := &rotator{path: filepath.Join(dir, FileName), max: max}
	if err := w.openFile(); err != nil {
		return nil, nil, err
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(redact.NewHandler(h)), w, nil
}

type rotator struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func (r *rotator) openFile() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		r.rotate() // best effort: on failure keep appending to the current file
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotator) rotate() {
	_ = r.f.Close()
	r.f = nil
	_ = os.Remove(r.path + "." + strconv.Itoa(Keep))
	for i := Keep - 1; i >= 1; i-- {
		_ = os.Rename(r.path+"."+strconv.Itoa(i), r.path+"."+strconv.Itoa(i+1))
	}
	_ = os.Rename(r.path, r.path+".1")
	if err := r.openFile(); err != nil {
		// last resort: reopen without losing the logger
		f, err2 := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err2 == nil {
			r.f, r.size = f, 0
		}
	}
}

func (r *rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
