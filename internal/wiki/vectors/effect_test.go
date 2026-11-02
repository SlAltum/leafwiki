package vectors

import (
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/wiki/pagesave"
)

func newTestSideEffect(t *testing.T) (*SideEffect, *Service, *embed.Store) {
	t.Helper()

	svc, store, _, _ := newTestService(t)
	return NewSideEffect(svc, nil), svc, store
}

func pageStub(id string) *tree.Page {
	return &tree.Page{PageNode: &tree.PageNode{ID: id}, Content: "## Section\n\nbody"}
}

func TestSideEffect_QueuesSavedPages(t *testing.T) {
	effect, svc, store := newTestSideEffect(t)

	effect.Apply(pagesave.PageSaveEvent{Operation: pagesave.PageOperationCreate, After: pageStub("p1")})
	effect.Apply(pagesave.PageSaveEvent{Operation: pagesave.PageOperationUpdate, After: pageStub("p1")})

	if count, err := store.QueueCount(); err != nil || count != 1 {
		t.Fatalf("queue holds %d pages, %v; want 1 after a create and an update", count, err)
	}

	if _, err := svc.Drain(0); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("queue holds %d pages after the drain, %v", count, err)
	}
}

func TestSideEffect_QueuesEveryMovedPage(t *testing.T) {
	effect, _, store := newTestSideEffect(t)

	effect.Apply(pagesave.PageSaveEvent{
		Operation:     pagesave.PageOperationMove,
		AffectedPages: []*tree.Page{pageStub("a"), pageStub("b")},
	})

	if count, err := store.QueueCount(); err != nil || count != 2 {
		t.Fatalf("queue holds %d pages, %v; want the moved subtree", count, err)
	}
}

func TestSideEffect_ForgetsDeletedPages(t *testing.T) {
	effect, svc, store := newTestSideEffect(t)

	effect.Apply(pagesave.PageSaveEvent{Operation: pagesave.PageOperationCreate, After: pageStub("p1")})
	if _, err := svc.Drain(0); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v", current, err)
	}
	if count, err := store.ChunkCount(current.ID); err != nil || count == 0 {
		t.Fatalf("ChunkCount = %d, %v", count, err)
	}

	effect.Apply(pagesave.PageSaveEvent{
		Operation:     pagesave.PageOperationDelete,
		AffectedPages: []*tree.Page{pageStub("p1")},
	})

	if count, err := store.ChunkCount(current.ID); err != nil || count != 0 {
		t.Fatalf("ChunkCount after delete = %d, %v; want the page's chunks gone", count, err)
	}
	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("queue holds %d pages after delete", count)
	}
}

func TestSideEffect_PausedServiceQueuesNothing(t *testing.T) {
	svc, store, _, _ := newTestService(t)
	effect := NewSideEffect(svc, nil)

	if err := svc.SetPaused(true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	effect.Apply(pagesave.PageSaveEvent{Operation: pagesave.PageOperationUpdate, After: pageStub("p1")})

	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("a paused service queued %d pages", count)
	}
}

func TestSideEffect_ManualModeQueuesNothing(t *testing.T) {
	store, err := embed.OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	svc := NewService(store, &fakeEncoder{}, &fakePages{}, "minilm", "wordpiece-1", ModeManual, nil)
	effect := NewSideEffect(svc, nil)

	effect.Apply(pagesave.PageSaveEvent{Operation: pagesave.PageOperationUpdate, After: pageStub("p1")})

	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("manual mode queued %d pages, want none", count)
	}
}

func TestSideEffect_RunsThroughTheOrchestrator(t *testing.T) {
	effect, _, store := newTestSideEffect(t)
	orchestrator := pagesave.NewPageSaveOrchestrator(nil, effect)

	orchestrator.Run(pagesave.PageSaveEvent{Operation: pagesave.PageOperationUpdate, After: pageStub("p1")})

	if count, err := store.QueueCount(); err != nil || count != 1 {
		t.Fatalf("orchestrator left %d queued pages, %v; want 1", count, err)
	}
}

func TestSideEffect_NilServiceIsANoOp(t *testing.T) {
	effect := NewSideEffect(nil, nil)
	effect.Apply(pagesave.PageSaveEvent{Operation: pagesave.PageOperationUpdate, After: pageStub("p1")})
}

func TestStore_DeletePageChunks(t *testing.T) {
	store, err := embed.OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	build, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(build, []embed.Chunk{
		{PageID: "a", ChunkNo: 0, ContentHash: "h", Vec: []float32{1}},
		{PageID: "b", ChunkNo: 0, ContentHash: "h", Vec: []float32{1}},
	}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}

	if err := store.DeletePageChunks("a"); err != nil {
		t.Fatalf("DeletePageChunks: %v", err)
	}
	if count, err := store.ChunkCount(build); err != nil || count != 1 {
		t.Fatalf("ChunkCount = %d, %v; want only page b", count, err)
	}
}
