package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if coded.code != vectorsExitLocked {
		t.Fatalf("exit code = %d, want %d", coded.code, vectorsExitLocked)
	}
}
