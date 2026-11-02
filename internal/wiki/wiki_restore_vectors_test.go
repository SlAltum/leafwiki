package wiki

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/embed"
)

// testLogger keeps a test's log output out of the test output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestForgetSemanticIndex_DropsTheVectors(t *testing.T) {
	store, err := embed.OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
	})

	build, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(build, []embed.Chunk{
		{PageID: "p1", ChunkNo: 0, ContentHash: "h", Vec: []float32{1, 0}},
	}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}
	if err := store.CommitBuild(build, "minilm", "wordpiece-1"); err != nil {
		t.Fatalf("CommitBuild: %v", err)
	}

	w := &Wiki{vectorStore: store, log: testLogger()}
	w.ForgetSemanticIndex()

	if current, err := store.CurrentBuild(); err != nil || current != nil {
		t.Fatalf("CurrentBuild after a restore = %#v, %v; want none", current, err)
	}
	if count, err := store.ChunkCount(build); err != nil || count != 0 {
		t.Fatalf("ChunkCount = %d, %v; want the vectors dropped", count, err)
	}
}

func TestForgetSemanticIndex_WithoutTheFeatureIsANoOp(t *testing.T) {
	w := &Wiki{log: testLogger()}
	w.ForgetSemanticIndex()
}
