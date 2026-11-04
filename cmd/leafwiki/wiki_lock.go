package main

import (
	"errors"

	"github.com/perber/wiki/internal/core/shared/advisorylock"
	"github.com/perber/wiki/internal/core/shared/wikilock"
)

// The data-directory lock lives in internal/core/shared/wikilock, because the
// desktop launcher takes it too. These wrappers keep the call sites short.

// wikiLockPath is the marker that says one process owns a data directory.
func wikiLockPath(dataDir string) string {
	return wikilock.Path(dataDir)
}

// ensureDataDir creates the data directory with the mode the server always used.
func ensureDataDir(dataDir string) error {
	return wikilock.EnsureDataDir(dataDir)
}

// acquireWikiLock takes the data directory for this process (FR-101).
func acquireWikiLock(dataDir string) (*advisorylock.File, error) {
	return wikilock.Acquire(dataDir)
}

// releaseWikiLock drops the marker.
func releaseWikiLock(lock *advisorylock.File) {
	wikilock.Release(lock)
}

// withWikiLock runs fn while this process owns the data directory.
func withWikiLock(dataDir string, fn func() error) error {
	lock, err := acquireWikiLock(dataDir)
	if err != nil {
		if errors.Is(err, advisorylock.ErrLocked) {
			return &exitCodeError{code: exitCodeLocked, err: err}
		}
		return err
	}
	defer releaseWikiLock(lock)
	return fn()
}
