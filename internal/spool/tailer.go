// Package spool tails append-only line files (a run's outbox.jsonl; later adapter stdout) by
// byte offset, tolerating partial lines, CRLF, BOMs, UTF-16 files, oversized lines and
// truncation. Dedupe by msg_id in the store makes offset commits crash-safe.
package spool

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/whosabhiraj/baton/internal/store"
)

// Warning codes produced by the tailer itself (store error codes are reused for rejected lines).
const (
	WarnUTF16       = "SPOOL_UTF16"
	WarnLineTooLong = "SPOOL_LINE_TOO_LONG"
	WarnTruncated   = "SPOOL_TRUNCATED"
)

// DefaultMaxLine is the longest accepted line (1 MiB).
const DefaultMaxLine = 1 << 20

// Tailer reads complete lines from Path starting at a persisted byte offset.
type Tailer struct {
	Path       string // file to tail
	OffsetPath string // where the committed offset is stored ("" = not persisted)
	MaxLine    int    // 0 = DefaultMaxLine
	// Handle receives each normalised, non-empty line and the byte offset of its start. A
	// non-nil error stops the batch without advancing past that line (it is retried).
	Handle func(line []byte, offset int64) error
	// Warn reports skipped input. May be nil.
	Warn func(code, msg string, offset int64)

	loaded bool
	off    int64
	// Done is set when the file was fully consumed (offset == size) at the last poll.
	Done bool
}

// Offset returns the current committed offset.
func (t *Tailer) Offset() int64 { return t.off }

func (t *Tailer) warn(code, msg string, off int64) {
	if t.Warn != nil {
		t.Warn(code, msg, off)
	}
}

func (t *Tailer) load() {
	t.loaded = true
	if t.OffsetPath == "" {
		return
	}
	b, err := os.ReadFile(t.OffsetPath)
	if err != nil {
		return
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && n >= 0 {
		t.off = n
	}
}

func (t *Tailer) commit() {
	if t.OffsetPath == "" {
		return
	}
	_ = store.WriteFileAtomic(t.OffsetPath, []byte(strconv.FormatInt(t.off, 10)+"\n"), 0o644)
}

// Poll reads and handles every complete line that appeared since the last poll, then commits
// the offset. A missing file is not an error.
func (t *Tailer) Poll() error {
	if !t.loaded {
		t.load()
	}
	fi, err := os.Stat(t.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	size := fi.Size()
	if size < t.off {
		t.warn(WarnTruncated, fmt.Sprintf("file is smaller (%d) than saved offset (%d); restarting from 0", size, t.off), t.off)
		t.off = 0
		t.commit()
	}
	if size == t.off {
		t.Done = true
		return nil
	}
	t.Done = false
	f, err := os.Open(t.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return err
	}
	max := t.MaxLine
	if max <= 0 {
		max = DefaultMaxLine
	}
	br := bufio.NewReaderSize(io.LimitReader(f, size-t.off), 64*1024)
	start := t.off
	start0 := t.off
	var herr error
	for {
		line, rawLen, tooLong, complete, rerr := readLine(br, max)
		if !complete {
			if rerr != nil && rerr != io.EOF {
				herr = rerr
			}
			break // partial last line: leave the offset before it
		}
		if tooLong {
			t.warn(WarnLineTooLong, fmt.Sprintf("line over %d bytes skipped", max), start)
		} else if t.isUTF16(line) {
			// The whole file is in the wrong encoding: warn once and consume what is there.
			t.warn(WarnUTF16, "outbox looks like UTF-16; write it as UTF-8 (PowerShell: Add-Content -Encoding utf8)", start)
			start = size
			t.off = size
			break
		} else if norm, ok := t.normalise(line, start); ok {
			if err := t.Handle(norm, start); err != nil {
				herr = err
				break
			}
		}
		start += rawLen
		t.off = start
	}
	if t.off != start0 {
		t.commit()
	}
	if t.off == size {
		t.Done = true
	}
	return herr
}

// readLine reads one LF-terminated line. rawLen is its length on disk including the
// terminator. If the line exceeds max bytes it is consumed but not returned (tooLong).
// complete is false if EOF came before a newline.
func readLine(br *bufio.Reader, max int) (line []byte, rawLen int64, tooLong, complete bool, err error) {
	var buf []byte
	for {
		chunk, rerr := br.ReadSlice(0x0A)
		rawLen += int64(len(chunk))
		if !tooLong {
			if len(buf)+len(chunk) > max+2 { // +2 for CRLF
				tooLong = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch {
		case rerr == nil:
			return buf, rawLen, tooLong, true, nil
		case errors.Is(rerr, bufio.ErrBufferFull):
			continue
		default:
			return nil, 0, false, false, rerr
		}
	}
}

func (t *Tailer) isUTF16(raw []byte) bool {
	return len(raw) >= 2 && (raw[0] == 0xFF && raw[1] == 0xFE || raw[0] == 0xFE && raw[1] == 0xFF) ||
		bytes.IndexByte(raw, 0) >= 0
}

// normalise strips the line terminator (LF / CRLF) and a UTF-8 BOM and skips blank lines.
func (t *Tailer) normalise(raw []byte, _ int64) ([]byte, bool) {
	raw = bytes.TrimSuffix(raw, []byte{0x0A})
	raw = bytes.TrimSuffix(raw, []byte{0x0D})
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	return raw, true
}
