package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/wiki/vectors"
)

func TestRunVectorsStatus_ReportsOffWithoutADatabase(t *testing.T) {
	cfg := &serverConfig{server: serverOptions{dataDir: t.TempDir()}}

	var out bytes.Buffer
	if err := runVectorsStatus(cfg, true, &out); err != nil {
		t.Fatalf("runVectorsStatus: %v", err)
	}

	var status vectors.Status
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if status.State != vectors.StateOff {
		t.Fatalf("state = %q, want off without a database", status.State)
	}
}

func TestRunVectorsStatus_TextOutput(t *testing.T) {
	cfg := &serverConfig{server: serverOptions{dataDir: t.TempDir()}}

	var out bytes.Buffer
	if err := runVectorsStatus(cfg, false, &out); err != nil {
		t.Fatalf("runVectorsStatus: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("state: off")) {
		t.Fatalf("output = %q, want a state line", out.String())
	}
}

func TestRunVectorsBuild_ReportsLockedWithItsExitCode(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	// Hold the lock the way a running server would.
	lock, err := acquireTestLock(dataDir)
	if err != nil {
		t.Fatalf("acquireTestLock: %v", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			t.Errorf("release: %v", releaseErr)
		}
	}()

	err = runVectorsBuild(t.Context(), cfg, true, "")
	var coded *exitCodeError
	if !errors.As(err, &coded) {
		t.Fatalf("runVectorsBuild = %v, want an exit code error", err)
	}
	if coded.code != exitCodeLocked {
		t.Fatalf("exit code = %d, want %d", coded.code, exitCodeLocked)
	}
}

// The commands that rewrite the data directory respect the same marker as the
// server (FR-101), so a live server is never written through.
func TestDataDirCommands_ReportLockedWhenTheDataDirIsInUse(t *testing.T) {
	dataDir := t.TempDir()
	lock, err := acquireTestWikiLock(dataDir)
	if err != nil {
		t.Fatalf("acquireTestWikiLock: %v", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			t.Errorf("release: %v", releaseErr)
		}
	}()

	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	for name, run := range map[string]func() error{
		"reset-admin-password": func() error { return runResetAdminPasswordCommand(cfg) },
		"restore-snapshot":     func() error { return runRestoreSnapshotCommand(dataDir, filepath.Join(dataDir, "snapshot.zip")) },
	} {
		err := run()
		var coded *exitCodeError
		if !errors.As(err, &coded) {
			t.Fatalf("%s = %v, want an exit code error", name, err)
		}
		if coded.code != exitCodeLocked {
			t.Fatalf("%s exit code = %d, want %d", name, coded.code, exitCodeLocked)
		}
	}
}

func TestRunVectorsBuild_ReportsLockedWhenTheDataDirIsInUse(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	// The whole data directory is the outer lock (FR-166): a running server
	// holds it, so an offline build must not touch the directory either.
	lock, err := acquireTestWikiLock(dataDir)
	if err != nil {
		t.Fatalf("acquireTestWikiLock: %v", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			t.Errorf("release: %v", releaseErr)
		}
	}()

	err = runVectorsBuild(t.Context(), cfg, true, "")
	var coded *exitCodeError
	if !errors.As(err, &coded) {
		t.Fatalf("runVectorsBuild = %v, want an exit code error", err)
	}
	if coded.code != exitCodeLocked {
		t.Fatalf("exit code = %d, want %d", coded.code, exitCodeLocked)
	}
}
