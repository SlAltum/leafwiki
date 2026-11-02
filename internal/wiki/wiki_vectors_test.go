package wiki

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// vectorsOffOptions is a minimal wiki configuration with semantic search at its
// default (off).
func vectorsOffOptions(storageDir string, mode string) *WikiOptions {
	return &WikiOptions{
		StorageDir:          storageDir,
		AdminPassword:       "adminpassword",
		JWTSecret:           "secretkey",
		AccessTokenTimeout:  15 * time.Minute,
		RefreshTokenTimeout: 7 * 24 * time.Hour,
		Vectors:             VectorsOptions{Mode: mode},
	}
}

func TestNewWiki_VectorsOffCreatesNothing(t *testing.T) {
	storageDir := t.TempDir()
	w, err := NewWiki(vectorsOffOptions(storageDir, ""))
	if err != nil {
		t.Fatalf("NewWiki: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	if _, statErr := os.Stat(filepath.Join(storageDir, "vectors.db")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a vectors database exists although the feature is off: %v", statErr)
	}

	status, err := w.VectorStatus()
	if err != nil {
		t.Fatalf("VectorStatus: %v", err)
	}
	if status.State != vectors.StateOff {
		t.Fatalf("state = %q, want off", status.State)
	}
}

func TestNewWiki_VectorsEnabled(t *testing.T) {
	storageDir := t.TempDir()
	w, err := NewWiki(vectorsOffOptions(storageDir, "auto"))

	if !embed.EncoderAvailable() {
		// An untagged build has no inference runtime: asking for the feature
		// must fail the start instead of running degraded by accident (C-7).
		if err == nil {
			_ = w.Close()
			t.Fatal("an untagged build accepted --vectors=auto")
		}
		return
	}
	if err != nil {
		t.Fatalf("NewWiki: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	if _, statErr := os.Stat(filepath.Join(storageDir, "vectors.db")); statErr != nil {
		t.Fatalf("the vectors database was not created: %v", statErr)
	}

	// No model was installed, so semantic search is degraded: the state says
	// so instead of pretending to be fresh (FR-402, FR-414).
	status, err := w.VectorStatus()
	if err != nil {
		t.Fatalf("VectorStatus: %v", err)
	}
	if status.State != vectors.StateMissing {
		t.Fatalf("state = %q, want missing without a model", status.State)
	}
}
