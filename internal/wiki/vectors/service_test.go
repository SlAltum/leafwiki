package vectors

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/embed"
)

// fakeEncoder counts calls and can hold a build open so the one-at-a-time rule
// is observable.
type fakeEncoder struct {
	mu      sync.Mutex
	calls   [][]string
	err     error
	entered chan struct{}
	release chan struct{}
}

func (f *fakeEncoder) Encode(texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), texts...))
	err := f.err
	entered, release := f.entered, f.release
	f.mu.Unlock()

	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return nil, err
	}

	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = []float32{float32(i + 1), 1}
	}
	return vectors, nil
}

func (f *fakeEncoder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakePages struct {
	pages map[string]*tree.Page
	order []string
}

func (f *fakePages) GetPage(id string) (*tree.Page, error) {
	page, ok := f.pages[id]
	if !ok {
		return nil, errors.New("page not found")
	}
	return page, nil
}

func (f *fakePages) WalkNodes(fn func(id string) error) error {
	for _, id := range f.order {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func newTestService(t *testing.T) (*Service, *embed.Store, *fakeEncoder, *fakePages) {
	t.Helper()

	store, err := embed.OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	pages := &fakePages{
		pages: map[string]*tree.Page{
			"p1": {Content: "## 部署流程\n\nrun deploy\n\n## Rollback\n\ngo back"},
			"p2": {Content: "intro only page"},
		},
		order: []string{"p1", "p2"},
	}
	encoder := &fakeEncoder{}
	return NewService(store, encoder, pages, "minilm", "wordpiece-1", ModeAuto, nil), store, encoder, pages
}

func TestEnqueue_MergesRepeatedSavesIntoOneEncode(t *testing.T) {
	svc, store, encoder, _ := newTestService(t)

	for range 3 {
		if err := svc.Enqueue("p1"); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	if count, err := store.QueueCount(); err != nil || count != 1 {
		t.Fatalf("queue holds %d entries, %v; want 1", count, err)
	}

	written, err := svc.Drain(0)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if written == 0 {
		t.Fatal("Drain wrote no chunks")
	}
	if calls := encoder.callCount(); calls != 1 {
		t.Fatalf("encoder ran %d times, want once for three saves", calls)
	}
	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("queue holds %d entries after the drain, %v", count, err)
	}

	current, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("CurrentBuild: %v", err)
	}
	if current == nil || current.ModelID != "minilm" || current.TokenizerVersion != "wordpiece-1" {
		t.Fatalf("CurrentBuild = %#v", current)
	}
}

func TestPaused_BlocksEnqueueAndResumes(t *testing.T) {
	svc, store, encoder, _ := newTestService(t)

	if err := svc.SetPaused(true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if paused, err := svc.Paused(); err != nil || !paused {
		t.Fatalf("Paused = %v, %v", paused, err)
	}
	if err := svc.Enqueue("p1"); err != nil {
		t.Fatalf("Enqueue while paused: %v", err)
	}
	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("a paused service queued %d pages", count)
	}
	if _, err := svc.Drain(0); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if encoder.callCount() != 0 {
		t.Fatal("a paused service encoded something")
	}

	if err := svc.SetPaused(false); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if err := svc.Enqueue("p1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := svc.Drain(0); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if encoder.callCount() != 1 {
		t.Fatalf("encoder ran %d times after resuming, want 1", encoder.callCount())
	}
}

func TestRebuild_IndexesEveryPage(t *testing.T) {
	svc, store, _, _ := newTestService(t)

	written, err := svc.Rebuild(nil)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if written != 3 {
		t.Fatalf("Rebuild wrote %d chunks, want 3 (two for p1, one for p2)", written)
	}

	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v", current, err)
	}
	if count, err := store.ChunkCount(current.ID); err != nil || count != 3 {
		t.Fatalf("ChunkCount = %d, %v", count, err)
	}
}

func TestIndexPage_KeepsTheRestOfTheIndex(t *testing.T) {
	svc, store, _, pages := newTestService(t)

	if _, err := svc.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	before, err := store.CurrentBuild()
	if err != nil || before == nil {
		t.Fatalf("CurrentBuild = %#v, %v", before, err)
	}

	// Re-embedding one page must not throw the other pages' chunks away.
	pages.pages["p1"] = &tree.Page{Content: "## Only one section now"}
	if err := svc.IndexPage("p1"); err != nil {
		t.Fatalf("IndexPage: %v", err)
	}

	after, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("CurrentBuild: %v", err)
	}
	if after == nil || after.ID != before.ID {
		t.Fatalf("a single-page reindex started a new generation: %#v -> %#v", before, after)
	}
	if count, err := store.ChunkCount(after.ID); err != nil || count != 2 {
		t.Fatalf("ChunkCount = %d, %v; want p1's single chunk plus p2's", count, err)
	}
}

func TestBuild_MissingPageDoesNotStopTheBatch(t *testing.T) {
	svc, store, encoder, pages := newTestService(t)
	delete(pages.pages, "p2")

	written, err := svc.Rebuild(nil)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if written != 2 {
		t.Fatalf("Rebuild wrote %d chunks, want the healthy page's 2", written)
	}
	if calls := encoder.callCount(); calls != 1 {
		t.Fatalf("encoder ran %d times, want 1", calls)
	}
	if current, err := store.CurrentBuild(); err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v", current, err)
	}
}

func TestService_SearchPages(t *testing.T) {
	svc, _, encoder, _ := newTestService(t)

	// Nothing to recall before a build exists.
	if hits, err := svc.SearchPages("dogs", 0); err != nil || len(hits) != 0 {
		t.Fatalf("SearchPages without a build returned %d hits, %v", len(hits), err)
	}

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	callsBefore := encoder.callCount()

	hits, err := svc.SearchPages("dogs", 0)
	if err != nil {
		t.Fatalf("SearchPages: %v", err)
	}
	if encoder.callCount() != callsBefore+1 {
		t.Fatalf("the query was encoded %d times, want once", encoder.callCount()-callsBefore)
	}
	if len(hits) == 0 {
		t.Fatal("SearchPages recalled nothing from a built index")
	}
	for _, hit := range hits {
		if hit.PageID != "p1" {
			t.Fatalf("hit page = %q, want p1", hit.PageID)
		}
	}
}

func TestService_SearchPages_StopsWhenTheBuildIsAnotherModels(t *testing.T) {
	_, store, _, pages := newTestService(t)
	other := NewService(store, &fakeEncoder{}, pages, "other-model", TokenizerVersion, ModeAuto, nil)

	if hits, err := other.SearchPages("dogs", 0); err != nil || len(hits) != 0 {
		t.Fatalf("a service built from another model recalled %d hits, %v", len(hits), err)
	}
}

func TestService_SearchPages_DegradesWhenSaturated(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// Fill every slot the way concurrent requests would.
	for i := 0; i < SemanticMaxConcurrency; i++ {
		svc.searchSlots <- struct{}{}
	}

	hits, err := svc.SearchPages("dogs", 0)
	if err != nil {
		t.Fatalf("SearchPages: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("a saturated service recalled %d pages, want none (degrade to keyword)", len(hits))
	}

	// Release one slot: recall works again.
	<-svc.searchSlots
	if hits, err := svc.SearchPages("dogs", 0); err != nil || len(hits) == 0 {
		t.Fatalf("SearchPages after releasing a slot = %d hits, %v", len(hits), err)
	}
}

func TestStartRebuild_EmbedsEveryPage(t *testing.T) {
	svc, store, _, pages := newTestService(t)
	// The fake offers exactly the pages newTestService seeded.
	_ = pages

	if err := svc.StartRebuild(); err != nil {
		t.Fatalf("StartRebuild: %v", err)
	}

	// The build runs in the background; wait for it to leave the running state.
	for i := 0; i < 200; i++ {
		status, err := svc.Status(true)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.State == StateFresh {
			current, err := store.CurrentBuild()
			if err != nil || current == nil {
				t.Fatalf("CurrentBuild = %#v, %v", current, err)
			}
			count, err := store.ChunkCount(current.ID)
			if err != nil {
				t.Fatalf("ChunkCount: %v", err)
			}
			if count == 0 {
				t.Fatal("a rebuild over a non-empty wiki committed an empty index")
			}
			return
		}
		if status.State == StateFailed {
			t.Fatal("the rebuild failed")
		}
	}
	t.Fatal("the background rebuild never finished")
}

func TestRebuild_EmptyPageListKeepsTheCurrentBuild(t *testing.T) {
	svc, store, _, _ := newTestService(t)
	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	before, err := store.CurrentBuild()
	if err != nil || before == nil {
		t.Fatalf("CurrentBuild = %#v, %v", before, err)
	}

	// An empty page list must never look like a successful rebuild: it would
	// swap the pointer and drop every chunk.
	if _, err := svc.Rebuild([]string{}); err == nil {
		t.Fatal("an empty rebuild reported success")
	}

	after, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("CurrentBuild: %v", err)
	}
	if after == nil || after.ID != before.ID {
		t.Fatalf("the current build changed to %#v", after)
	}
}

func TestPageStatus(t *testing.T) {
	svc, _, _, pages := newTestService(t)

	// Not embedded yet: missing.
	if status, err := svc.PageStatus("p1"); err != nil || status.State != PageMissing {
		t.Fatalf("PageStatus = %#v, %v; want missing", status, err)
	}

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	status, err := svc.PageStatus("p1")
	if err != nil {
		t.Fatalf("PageStatus: %v", err)
	}
	if status.State != PageIndexed {
		t.Fatalf("state = %q, want indexed after a rebuild", status.State)
	}
	if status.Passages == 0 {
		t.Fatal("an indexed page reports no passages")
	}

	// Editing the page makes the stored passages stale, without any rebuild.
	edited := pages.pages["p1"]
	edited.Content = edited.Content + "\n\n## 新增小节\n\n这段内容还没进索引。"
	fresh, err := svc.PageStatus("p1")
	if err != nil {
		t.Fatalf("PageStatus: %v", err)
	}
	if fresh.State != PageStale {
		t.Fatalf("state = %q, want stale after an edit", fresh.State)
	}

	// Queueing the page is what a save does; it outranks stale.
	if err := svc.Enqueue("p1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	queued, err := svc.PageStatus("p1")
	if err != nil {
		t.Fatalf("PageStatus: %v", err)
	}
	if queued.State != PageQueued {
		t.Fatalf("state = %q, want queued", queued.State)
	}

	// A failed queue row means nothing retries the page by itself.
	if err := svc.store.MarkQueuedFailure("p1", "boom"); err != nil {
		t.Fatalf("MarkQueuedFailure: %v", err)
	}
	failed, err := svc.PageStatus("p1")
	if err != nil {
		t.Fatalf("PageStatus: %v", err)
	}
	if failed.State != PageFailed || failed.LastError != "boom" {
		t.Fatalf("status = %#v, want failed with its error", failed)
	}
}

func TestPageStatus_RebuildingOnePageLeavesTheOthersAlone(t *testing.T) {
	svc, store, _, pages := newTestService(t)
	if _, err := svc.Rebuild([]string{"p1", "p2"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v", current, err)
	}
	before, err := store.ChunkCount(current.ID)
	if err != nil {
		t.Fatalf("ChunkCount: %v", err)
	}

	pages.pages["p2"].Content = "全是新内容。" + pages.pages["p2"].Content
	if err := svc.IndexPage("p2"); err != nil {
		t.Fatalf("IndexPage: %v", err)
	}

	after, err := store.ChunkCount(current.ID)
	if err != nil {
		t.Fatalf("ChunkCount: %v", err)
	}
	if after == 0 {
		t.Fatal("indexing one page emptied the build")
	}
	if status, err := svc.PageStatus("p1"); err != nil || status.State != PageIndexed {
		t.Fatalf("the untouched page = %#v, %v; want still indexed", status, err)
	}
	if status, err := svc.PageStatus("p2"); err != nil || status.State != PageIndexed {
		t.Fatalf("the rebuilt page = %#v, %v; want indexed", status, err)
	}
	if after != before {
		t.Fatalf("chunk count %d -> %d; rebuilding one page must not resize the build", before, after)
	}
}

func TestBuild_OneBuildAtATime(t *testing.T) {
	svc, _, encoder, _ := newTestService(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	encoder.entered = entered
	encoder.release = release

	done := make(chan error, 1)
	go func() {
		_, err := svc.Rebuild([]string{"p1"})
		done <- err
	}()

	<-entered
	if _, err := svc.Rebuild([]string{"p1"}); !errors.Is(err, ErrBuildRunning) {
		t.Fatalf("second build = %v, want ErrBuildRunning", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first build: %v", err)
	}
}

func TestBuild_WithoutEncoder(t *testing.T) {
	_, store, _, pages := newTestService(t)
	svc := NewService(store, nil, pages, "minilm", "wordpiece-1", ModeAuto, nil)

	if _, err := svc.Rebuild(nil); !errors.Is(err, ErrEncoderUnavailable) {
		t.Fatalf("Rebuild = %v, want ErrEncoderUnavailable", err)
	}
	if err := svc.IndexPage("p1"); !errors.Is(err, ErrEncoderUnavailable) {
		t.Fatalf("IndexPage = %v, want ErrEncoderUnavailable", err)
	}
}
