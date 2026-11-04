// Package wikilock owns the single-writer marker of a data directory: the server,
// the offline commands and the desktop launcher take the same file, so a second
// writer is refused (docs/srs/agent-operations.srs.md FR-101).
package wikilock

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/perber/wiki/internal/core/shared/advisorylock"
)

// Path returns wiki.lock, the file that marks a data directory as taken.
func Path(dataDir string) string {
	return filepath.Join(dataDir, "wiki.lock")
}

// EnsureDataDir creates the data directory when it is missing. The lock file
// lives inside it, and advisorylock would otherwise create the chain with its
// own 0700, silently changing what --data-dir leaves behind.
func EnsureDataDir(dataDir string) error {
	if _, err := os.Stat(dataDir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	slog.Default().Info("Data directory created", "path", dataDir)
	return nil
}

// Acquire takes the data directory for this process; a second writer gets
// advisorylock.ErrLocked.
func Acquire(dataDir string) (*advisorylock.File, error) {
	lock, err := advisorylock.TryAcquire(Path(dataDir))
	if err != nil {
		if errors.Is(err, advisorylock.ErrLocked) {
			return nil, fmt.Errorf("another instance is using the data directory %s: %w", dataDir, err)
		}
		return nil, fmt.Errorf("cannot lock the data directory %s: %w", dataDir, err)
	}
	return lock, nil
}

// Release drops the marker. A release failure is logged, not returned: the
// process is ending and the kernel drops the lock with the handle anyway.
func Release(lock *advisorylock.File) {
	if err := lock.Release(); err != nil {
		slog.Default().Warn("could not release the wiki lock", "error", err)
	}
}
