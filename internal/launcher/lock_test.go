package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquire_SecondHolderIsLockedOut(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	if _, err := Acquire(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire() error = %v, want ErrLocked", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	again, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire() after Release() error = %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

func TestWriteState_RoundTripsThenRemoves(t *testing.T) {
	dir := t.TempDir()
	state := State{PID: 4711, Port: 51234, BasePath: "/wiki", StartedAt: StateTimestamp(time.Now())}

	if err := WriteState(dir, state); err != nil {
		t.Fatalf("WriteState() error = %v", err)
	}

	loaded, err := ReadState(dir)
	if err != nil {
		t.Fatalf("ReadState() error = %v", err)
	}
	if loaded != state {
		t.Fatalf("ReadState() = %+v, want %+v", loaded, state)
	}

	if err := RemoveState(dir); err != nil {
		t.Fatalf("RemoveState() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, stateFileName)); !os.IsNotExist(err) {
		t.Fatalf("state file still there (stat err = %v)", err)
	}
	if err := RemoveState(dir); err != nil {
		t.Fatalf("RemoveState() second call error = %v", err)
	}
}

func TestDecideLocked_ThreeBranches(t *testing.T) {
	answers := func() bool { return true }
	silent := func() bool { return false }

	if got := DecideLocked(State{PID: 100}, 42, answers); got != HandOver {
		t.Fatalf("live instance = %v, want HandOver", got)
	}
	if got := DecideLocked(State{PID: 100}, 42, silent); got != AnotherInstance {
		t.Fatalf("unresponsive instance = %v, want AnotherInstance", got)
	}
	if got := DecideLocked(State{PID: 42}, 42, answers); got != SelfPID {
		t.Fatalf("own pid = %v, want SelfPID", got)
	}
}
