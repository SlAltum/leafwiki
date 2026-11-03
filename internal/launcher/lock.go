package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/perber/wiki/internal/core/shared"
	"github.com/perber/wiki/internal/core/shared/advisorylock"
)

const (
	lockFileName  = "launcher.lock"
	stateFileName = "launcher.state.json"
)

// ErrLocked means another launcher owns this data directory.
var ErrLocked = errors.New("the data directory is in use by another instance")

// Handle is the held data-directory lock. One process takes it once and keeps
// it for its whole life: stopping and restarting the server does not release it
// (SRS §6.4).
type Handle struct {
	file *advisorylock.File
}

// Acquire takes the data-directory lock, or reports ErrLocked when another
// process holds it.
func Acquire(dataDir string) (*Handle, error) {
	file, err := advisorylock.TryAcquire(filepath.Join(dataDir, lockFileName))
	if err != nil {
		if errors.Is(err, advisorylock.ErrLocked) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("acquire %s: %w", lockFileName, err)
	}
	return &Handle{file: file}, nil
}

// Release gives the lock back. The kernel does this on process exit anyway.
func (h *Handle) Release() error {
	if h == nil {
		return nil
	}
	return h.file.Release()
}

// State is what the running instance advertises to the next one that finds the
// data directory locked (SRS §6.4).
type State struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	BasePath  string `json:"base_path"`
	StartedAt string `json:"started_at"`
}

// ReadState reads the advertised state. A missing or unreadable file is an
// error the caller treats as "someone else holds the lock, we know nothing".
func ReadState(dataDir string) (State, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, stateFileName))
	if err != nil {
		return State{}, fmt.Errorf("read %s: %w", stateFileName, err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("parse %s: %w", stateFileName, err)
	}
	return state, nil
}

// WriteState advertises where the running server can be reached.
func WriteState(dataDir string, state State) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", stateFileName, err)
	}
	if err := shared.WriteFileAtomic(filepath.Join(dataDir, stateFileName), append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", stateFileName, err)
	}
	return nil
}

// RemoveState drops the advertisement. It is called when the server stops and
// before a forced exit, so the next start cannot mistake it for a live instance.
func RemoveState(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, stateFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// StateTimestamp formats the StartedAt field.
func StateTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// LockedAction says what a start attempt does when another process holds the
// lock (SRS §6.4's three branches).
type LockedAction int

const (
	// HandOver means the other instance answered on its advertised port: open
	// the browser on it and exit normally.
	HandOver LockedAction = iota
	// AnotherInstance means it did not answer in time: report a failed start
	// the user can retry.
	AnotherInstance
	// SelfPID means the state names this very process, which means leftover
	// state rather than a live second instance.
	SelfPID
)

// DecideLocked is the pure part of the locked branch: probe reports whether the
// advertised instance answers right now.
func DecideLocked(state State, myPID int, probe func() bool) LockedAction {
	if myPID != 0 && state.PID == myPID {
		return SelfPID
	}
	if probe != nil && probe() {
		return HandOver
	}
	return AnotherInstance
}
