package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModel writes a model directory with the given files and a manifest whose
// digests match them.
func writeModel(t *testing.T, modelsDir, modelID string, files map[string]string) string {
	t.Helper()

	dir := filepath.Join(modelsDir, modelID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	manifest := Manifest{
		ModelID:   modelID,
		Dim:       384,
		DType:     "float32",
		Tokenizer: "wordpiece",
		MaxTokens: 512,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
		sum := sha256.Sum256([]byte(content))
		manifest.Files = append(manifest.Files, File{Name: name, SHA256: hex.EncodeToString(sum[:])})
	}
	writeManifest(t, dir, manifest)
	return dir
}

func writeManifest(t *testing.T, dir string, manifest Manifest) {
	t.Helper()

	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestJSON), raw, 0o600); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
}

func TestOpen(t *testing.T) {
	modelsDir := t.TempDir()
	dir := writeModel(t, modelsDir, "minilm", map[string]string{"model.onnx": "weights"})

	model, err := Open(modelsDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if model.Dir != dir {
		t.Fatalf("Dir = %q, want %q", model.Dir, dir)
	}
	if model.Manifest.ModelID != "minilm" || model.Manifest.Dim != 384 {
		t.Fatalf("Manifest = %#v", model.Manifest)
	}
	if err := model.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestOpen_Unavailable(t *testing.T) {
	t.Run("models directory does not exist", func(t *testing.T) {
		if _, err := Open(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrModelUnavailable) {
			t.Fatalf("Open = %v, want ErrModelUnavailable", err)
		}
	})

	t.Run("no subdirectory holds a manifest", func(t *testing.T) {
		modelsDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(modelsDir, "empty"), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if _, err := Open(modelsDir); !errors.Is(err, ErrModelUnavailable) {
			t.Fatalf("Open = %v, want ErrModelUnavailable", err)
		}
	})
}

func TestOpen_Ambiguous(t *testing.T) {
	modelsDir := t.TempDir()
	writeModel(t, modelsDir, "model-a", map[string]string{"model.onnx": "a"})
	writeModel(t, modelsDir, "model-b", map[string]string{"model.onnx": "b"})

	if _, err := Open(modelsDir); !errors.Is(err, ErrModelAmbiguous) {
		t.Fatalf("Open = %v, want ErrModelAmbiguous", err)
	}
}

func TestOpen_InvalidManifest(t *testing.T) {
	valid := func() Manifest {
		return Manifest{
			ModelID:   "minilm",
			Dim:       384,
			DType:     "float32",
			Tokenizer: "wordpiece",
			MaxTokens: 512,
			Files:     []File{{Name: "model.onnx", SHA256: strings.Repeat("0", 64)}},
		}
	}

	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"empty model id", func(m *Manifest) { m.ModelID = " " }},
		{"missing dim", func(m *Manifest) { m.Dim = 0 }},
		{"unknown dtype", func(m *Manifest) { m.DType = "bfloat9" }},
		{"empty tokenizer", func(m *Manifest) { m.Tokenizer = "" }},
		{"negative max tokens", func(m *Manifest) { m.MaxTokens = -1 }},
		{"no files", func(m *Manifest) { m.Files = nil }},
		{"empty file name", func(m *Manifest) { m.Files[0].Name = "" }},
		{"path in file name", func(m *Manifest) { m.Files[0].Name = "../model.onnx" }},
		{"short sha256", func(m *Manifest) { m.Files[0].SHA256 = "abc" }},
		{"non-hex sha256", func(m *Manifest) { m.Files[0].SHA256 = strings.Repeat("z", 64) }},
		{"duplicate file", func(m *Manifest) { m.Files = append(m.Files, m.Files[0]) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			modelsDir := t.TempDir()
			dir := filepath.Join(modelsDir, "minilm")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			manifest := valid()
			tc.mutate(&manifest)
			writeManifest(t, dir, manifest)

			if _, err := Open(modelsDir); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Open = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestOpen_MalformedJSON(t *testing.T) {
	modelsDir := t.TempDir()
	dir := filepath.Join(modelsDir, "minilm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestJSON), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Open(modelsDir); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Open = %v, want ErrInvalidManifest", err)
	}
}

func TestVerify_ChecksumMismatch(t *testing.T) {
	modelsDir := t.TempDir()
	writeModel(t, modelsDir, "minilm", map[string]string{"model.onnx": "weights"})

	model, err := Open(modelsDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.WriteFile(filepath.Join(model.Dir, "model.onnx"), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := model.Verify(); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Verify = %v, want ErrChecksumMismatch", err)
	}
}

func TestVerify_MissingFile(t *testing.T) {
	modelsDir := t.TempDir()
	writeModel(t, modelsDir, "minilm", map[string]string{"model.onnx": "weights"})

	model, err := Open(modelsDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.Remove(filepath.Join(model.Dir, "model.onnx")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if err := model.Verify(); err == nil {
		t.Fatal("Verify succeeded for a missing file")
	}
}
