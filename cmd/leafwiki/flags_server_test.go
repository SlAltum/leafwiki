package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A shipped bundle is leafwiki.exe next to data/: the executable finds that
// directory itself, so an agent needs no --data-dir and no prior knowledge.
func TestDataDirNextTo_BundleLayout(t *testing.T) {
	bundle := t.TempDir()
	dataDir := filepath.Join(bundle, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "root"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := dataDirNextTo(filepath.Join(bundle, "leafwiki.exe"))
	if got != dataDir {
		t.Fatalf("dataDirNextTo() = %q, want %q", got, dataDir)
	}
}

// A source checkout, or a directory holding a file called "data", keeps ./data.
func TestDataDirNextTo_FallsBackToWorkingDirectory(t *testing.T) {
	empty := t.TempDir()
	if got := dataDirNextTo(filepath.Join(empty, "leafwiki.exe")); got != "./data" {
		t.Fatalf("dataDirNextTo() = %q, want ./data", got)
	}

	notADir := t.TempDir()
	if err := os.WriteFile(filepath.Join(notADir, "data"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := dataDirNextTo(filepath.Join(notADir, "leafwiki.exe")); got != "./data" {
		t.Fatalf("dataDirNextTo() = %q for a file named data, want ./data", got)
	}
}

// An explicit --data-dir still wins over the bundle default.
func TestDataDirFlag_ExplicitValueWins(t *testing.T) {
	cfg, _ := newServerConfigForTest(t, "--data-dir=/srv/wiki")
	if cfg.server.dataDir != "/srv/wiki" {
		t.Fatalf("dataDir = %q, want the flag value", cfg.server.dataDir)
	}
}
