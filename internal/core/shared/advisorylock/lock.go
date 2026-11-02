// Package advisorylock takes an OS advisory lock on a file, so the running
// server and an offline command cannot write the same derived database at the
// same time. It is an OS lock, not a PID file: a crashed process releases it
// when the kernel closes the handle (FR-412).
package advisorylock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("advisorylock: the lock is held by another process")

// File is a held advisory lock.
type File struct {
	file *os.File
	path string
}

// TryAcquire opens path (creating it when missing) and takes an exclusive,
// non-blocking lock. It returns ErrLocked instead of waiting.
func TryAcquire(path string) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("advisorylock: preparing %s: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("advisorylock: opening %s: %w", path, err)
	}

	if err := lockFile(file); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	return &File{file: file, path: path}, nil
}

// Release unlocks and closes the lock file.
func (f *File) Release() error {
	if f == nil || f.file == nil {
		return nil
	}
	unlockErr := unlockFile(f.file)
	closeErr := f.file.Close()
	f.file = nil
	return errors.Join(unlockErr, closeErr)
}

// Path returns the lock file path, for messages.
func (f *File) Path() string {
	if f == nil {
		return ""
	}
	return f.path
}
