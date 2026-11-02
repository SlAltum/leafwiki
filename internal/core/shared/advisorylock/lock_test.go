package advisorylock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTryAcquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db.lock")

	first, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}

	// A second holder must be refused instead of waiting.
	if _, err := TryAcquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryAcquire = %v, want ErrLocked", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestRelease_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db.lock")
	lock, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	var nilLock *File
	if err := nilLock.Release(); err != nil {
		t.Fatalf("nil Release: %v", err)
	}
}
