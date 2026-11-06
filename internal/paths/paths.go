// Package paths computes where Baton keeps its data. Directory names are stable: changing
// the naming would orphan existing project data (see the golden tests).
package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// File names inside a run directory.
const (
	RunRecordFile    = "run.json"
	EventsFile       = "events.jsonl"
	OutboxFile       = "outbox.jsonl"
	OutboxOffsetFile = "outbox.offset"
	InboxDir         = "inbox"
	InboxReadFile    = ".read"
)

// BatonHome returns $BATON_HOME (made absolute) or ~/.baton.
func BatonHome() string {
	if h := os.Getenv("BATON_HOME"); h != "" {
		if abs, err := filepath.Abs(h); err == nil {
			return abs
		}
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".baton")
}

// ProjectDataDir returns <home>/projects/<name>-<h8> for a repo, where h8 is the first 8 hex
// chars of sha256 of the real path (lower-cased on Windows and macOS) and name is the
// sanitised, 40-char-capped base name of the real path. Symlinks are resolved when the path
// exists.
func ProjectDataDir(repoPath, home string) string {
	return projectDataDir(repoPath, home, runtime.GOOS)
}

func projectDataDir(repoPath, home, goos string) string {
	real, err := filepath.Abs(repoPath)
	if err != nil {
		real = filepath.Clean(repoPath)
	}
	if r, err := filepath.EvalSymlinks(real); err == nil {
		real = r
	}
	return filepath.Join(home, "projects", dirName(real, goos))
}

// dirName builds "<name>-<h8>" for an absolute, symlink-free path.
func dirName(real, goos string) string {
	key := real
	if goos == "windows" || goos == "darwin" {
		key = strings.ToLower(real)
	}
	sum := sha256.Sum256([]byte(key))
	return sanitizeName(filepath.Base(real)) + "-" + hex.EncodeToString(sum[:])[:8]
}

// sanitizeName replaces every character outside [A-Za-z0-9._-] by "_" (astral characters
// count twice, like UTF-16 units in the TypeScript original) and caps the result at 40.
func sanitizeName(base string) string {
	if base == "." || base == "" || base == string(filepath.Separator) || base == "/" || base == `\` {
		return "root"
	}
	var sb strings.Builder
	for _, r := range base {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			sb.WriteRune(r)
		case r > 0xFFFF:
			sb.WriteString("__")
		default:
			sb.WriteByte('_')
		}
	}
	name := sb.String()
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

// RunsDir returns <dataDir>/runs.
func RunsDir(dataDir string) string { return filepath.Join(dataDir, "runs") }

// RunDir returns <dataDir>/runs/<runID>.
func RunDir(dataDir, runID string) string { return filepath.Join(RunsDir(dataDir), runID) }

// InboxFileName returns the inbox file name for message n, e.g. 12 -> "000012.json".
func InboxFileName(n int) string { return fmt.Sprintf("%06d.json", n) }

var inboxName = regexp.MustCompile(`^(\d{6,})\.json$`)

// ParseInboxFileName extracts n from an inbox file name ("000012.json"). Temp files and
// anything else report false.
func ParseInboxFileName(name string) (int, bool) {
	m := inboxName.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
