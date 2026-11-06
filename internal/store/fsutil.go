package store

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"
)

// WriteFileAtomic writes data to path via a temp file in the same directory, fsyncs it and
// renames it over path. On Windows a rename can fail transiently (antivirus, indexer), so it
// is retried up to 5 times with a 20 ms backoff.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(dir, filepath.Base(path)+".tmp-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	for i := 0; ; i++ {
		err = os.Rename(tmp, path)
		if err == nil {
			return nil
		}
		if i >= 4 {
			_ = os.Remove(tmp)
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}
