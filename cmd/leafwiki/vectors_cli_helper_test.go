package main

import (
	"testing"

	"github.com/perber/wiki/internal/core/shared/advisorylock"
)

// acquireTestLock takes the vectors lock the way a running server does.
func acquireTestLock(dataDir string) (*advisorylock.File, error) {
	return advisorylock.TryAcquire(vectorsLockPath(dataDir))
}

// acquireTestWikiLock takes the data directory lock the way a running server
// does.
func acquireTestWikiLock(dataDir string) (*advisorylock.File, error) {
	return advisorylock.TryAcquire(wikiLockPath(dataDir))
}

// mustWikiLock hands a server under test its data directory lock.
func mustWikiLock(t *testing.T, dataDir string) *advisorylock.File {
	t.Helper()

	lock, err := acquireWikiLock(dataDir)
	if err != nil {
		t.Fatalf("acquireWikiLock(%s): %v", dataDir, err)
	}
	return lock
}
