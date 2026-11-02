package main

import "github.com/perber/wiki/internal/core/shared/advisorylock"

// acquireTestLock takes the vectors lock the way a running server does.
func acquireTestLock(dataDir string) (*advisorylock.File, error) {
	return advisorylock.TryAcquire(vectorsLockPath(dataDir))
}
