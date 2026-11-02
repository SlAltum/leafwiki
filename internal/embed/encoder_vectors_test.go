//go:build vectors

package embed

import (
	"math"
	"os"
	"testing"
)

// dot is a cosine, because the encoder normalizes.
func dot(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

// TestEncoderEndToEnd runs the real model through ONNX Runtime. It needs a
// prepared model directory and the runtime library, so it skips unless both are
// configured:
//
//	LEAFWIKI_VECTORS_TEST_MODELS_DIR  the directory holding the model directory
//	LEAFWIKI_VECTORS_TEST_LIBRARY     path to libonnxruntime
func TestEncoderEndToEnd(t *testing.T) {
	modelsDir := os.Getenv("LEAFWIKI_VECTORS_TEST_MODELS_DIR")
	library := os.Getenv("LEAFWIKI_VECTORS_TEST_LIBRARY")
	if modelsDir == "" || library == "" {
		t.Skip("set LEAFWIKI_VECTORS_TEST_MODELS_DIR and LEAFWIKI_VECTORS_TEST_LIBRARY to run the encoder against a real model")
	}

	model, err := Open(modelsDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	encoder, err := NewEncoder(model, EncoderOptions{SharedLibraryPath: library})
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	t.Cleanup(func() {
		if err := encoder.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	vectors, err := encoder.Encode([]string{"a dog is running", "a puppy runs", "the stock market crashed"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(vectors) != 3 {
		t.Fatalf("Encode returned %d vectors, want 3", len(vectors))
	}
	for i, vector := range vectors {
		if len(vector) != model.Manifest.Dim {
			t.Fatalf("vector %d has %d dimensions, want %d", i, len(vector), model.Manifest.Dim)
		}
		if norm := math.Sqrt(dot(vector, vector)); math.Abs(norm-1) > 1e-4 {
			t.Fatalf("vector %d has norm %f, want 1", i, norm)
		}
	}

	related := dot(vectors[0], vectors[1])
	unrelated := dot(vectors[0], vectors[2])
	if related < 0.6 {
		t.Fatalf("cos(running dog, puppy) = %.4f, want the two sentences to be close", related)
	}
	// The absolute floor of an unrelated pair is a property of the model, not of
	// the encoder (a Chinese model scores English sentences higher than an
	// English one does), so the assertion is the ranking that must always hold.
	if unrelated >= related {
		t.Fatalf("cos(running dog, stock market) = %.4f is not below the related pair %.4f", unrelated, related)
	}
	if related-unrelated < 0.15 {
		t.Fatalf("the two pairs are %.4f apart, want a clear separation", related-unrelated)
	}
	t.Logf("cos(running dog, puppy runs) = %.4f, cos(running dog, stock market) = %.4f", related, unrelated)

	// The same text must produce the same vector in a batch or alone: the
	// padding mask has to be respected.
	alone, err := encoder.Encode([]string{"a puppy runs"})
	if err != nil {
		t.Fatalf("Encode single: %v", err)
	}
	if math.Abs(dot(alone[0], vectors[1])-1) > 1e-4 {
		t.Fatalf("a text encoded alone differs from the same text in a batch: %.6f", dot(alone[0], vectors[1]))
	}
}
