package embed

import (
	"math"
	"path/filepath"
	"testing"
)

func openSearchStore(t *testing.T) *Store {
	t.Helper()

	store, err := OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return store
}

func TestStore_SearchVector(t *testing.T) {
	store := openSearchStore(t)

	build, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(build, []Chunk{
		{PageID: "dog", ChunkNo: 0, Heading: "Dogs", Anchor: "dogs", ContentHash: "h", Vec: []float32{1, 0, 0}},
		{PageID: "dog", ChunkNo: 1, Heading: "Dogs", Anchor: "dogs", ContentHash: "h", Vec: []float32{0.9, 0.1, 0}},
		{PageID: "cat", ChunkNo: 0, Heading: "Cats", Anchor: "cats", ContentHash: "h", Vec: []float32{0.8, 0.2, 0}},
		{PageID: "stock", ChunkNo: 0, Heading: "Markets", Anchor: "markets", ContentHash: "h", Vec: []float32{0, 1, 0}},
	}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}

	hits, err := store.SearchVector(build, []float32{1, 0, 0}, 0.5, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("SearchVector returned %d hits, want one per matching page: %#v", len(hits), hits)
	}
	if hits[0].PageID != "dog" || hits[1].PageID != "cat" {
		t.Fatalf("order = %q, %q; want dog then cat", hits[0].PageID, hits[1].PageID)
	}
	// One hit per page, and it is the page's best passage.
	if hits[0].ChunkNo != 0 {
		t.Fatalf("dog hit chunk = %d, want its best passage 0", hits[0].ChunkNo)
	}
	if math.Abs(hits[0].Score-1) > 1e-6 {
		t.Fatalf("dog score = %f, want 1", hits[0].Score)
	}
	if hits[0].Anchor != "dogs" {
		t.Fatalf("anchor = %q, want the chunk's anchor", hits[0].Anchor)
	}

	// The similarity floor keeps the weaker passage out: cat normalizes to
	// about 0.97, so a floor of 0.99 leaves only dog.
	if hits, err := store.SearchVector(build, []float32{1, 0, 0}, 0.99, 10); err != nil || len(hits) != 1 {
		t.Fatalf("SearchVector with a floor returned %d hits, %v", len(hits), err)
	}

	// A limit cuts the tail, and an unknown build returns nothing.
	if hits, err := store.SearchVector(build, []float32{1, 0, 0}, 0.5, 1); err != nil || len(hits) != 1 {
		t.Fatalf("limited SearchVector returned %d hits, %v", len(hits), err)
	}
	if hits, err := store.SearchVector("missing", []float32{1, 0, 0}, 0.5, 10); err != nil || len(hits) != 0 {
		t.Fatalf("SearchVector on an unknown build returned %d hits, %v", len(hits), err)
	}
}

func TestStore_SearchVector_RejectsAnotherModelsVectors(t *testing.T) {
	store := openSearchStore(t)

	build, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(build, []Chunk{
		{PageID: "p", ChunkNo: 0, ContentHash: "h", Vec: []float32{1, 0}},
	}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}

	// A query from a 3-dimensional model must not be compared against
	// 2-dimensional vectors.
	if hits, err := store.SearchVector(build, []float32{1, 0, 0}, 0, 10); err != nil || len(hits) != 0 {
		t.Fatalf("mismatched dimensions returned %d hits, %v", len(hits), err)
	}
}
