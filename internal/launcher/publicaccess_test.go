package launcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePublicRead_WritesOnlyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsurePublicRead(dir); err != nil {
		t.Fatalf("EnsurePublicRead() error = %v", err)
	}

	path := filepath.Join(dir, publicAccessFileName)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", publicAccessFileName, err)
	}
	if string(body) != publicReadBody {
		t.Fatalf("body = %q, want %q", body, publicReadBody)
	}

	if err := os.WriteFile(path, []byte(`{"enabled": false}`), 0o600); err != nil {
		t.Fatalf("write %s: %v", publicAccessFileName, err)
	}
	if err := EnsurePublicRead(dir); err != nil {
		t.Fatalf("EnsurePublicRead() second call error = %v", err)
	}
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s again: %v", publicAccessFileName, err)
	}
	if string(kept) != `{"enabled": false}` {
		t.Fatalf("body = %q, want the existing value untouched (FR-303)", kept)
	}
}
