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
	// The first drain opens the first build, which covers every page: three
	// saves of one page must still cost one encode for it.
	if calls := encoder.callCount(); calls != 2 {
		t.Fatalf("encoder ran %d times, want once per page of the first build", calls)
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
	if _, err := svc.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	callsBefore := encoder.callCount()

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
	if calls := encoder.callCount() - callsBefore; calls != 0 {
		t.Fatalf("a paused service encoded %d times", calls)
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
	if calls := encoder.callCount() - callsBefore; calls != 1 {
		t.Fatalf("encoder ran %d times after resuming, want once for the queued page", calls)
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

func TestDrain_KeepsPagesThatWereNotQueued(t *testing.T) {
	svc, store, encoder, pages := newTestService(t)

	// Three pages in the index: a drain after one edit must leave the other two
	// answering from the same build (FR-164).
	pages.pages["p3"] = &tree.Page{Content: `## 第三页

dritte Seite`}
	pages.order = append(pages.order, "p3")
	if _, err := svc.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	before, err := store.CurrentBuild()
	if err != nil || before == nil {
		t.Fatalf("CurrentBuild = %#v, %v", before, err)
	}
	chunksBefore, err := store.ChunkCount(before.ID)
	if err != nil {
		t.Fatalf("ChunkCount: %v", err)
	}
	callsBefore := encoder.callCount()

	// Only one page changed, so only one page is queued. The edit keeps the
	// section count, so the index must keep its size.
	pages.pages["p1"] = &tree.Page{Content: "## 部署流程\n\nrun deploy again\n\n## Rollback\n\ngo back again"}
	if err := svc.Enqueue("p1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := svc.Drain(0); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if calls := encoder.callCount() - callsBefore; calls != 1 {
		t.Fatalf("the drain encoded %d times, want once for the one queued page", calls)
	}

	after, err := store.CurrentBuild()
	if err != nil || after == nil {
		t.Fatalf("CurrentBuild = %#v, %v", after, err)
	}
	if after.ID != before.ID {
		t.Fatalf("the drain switched the build generation %q -> %q", before.ID, after.ID)
	}
	chunksAfter, err := store.ChunkCount(after.ID)
	if err != nil {
		t.Fatalf("ChunkCount: %v", err)
	}
	if chunksAfter != chunksBefore {
		t.Fatalf("chunk count %d -> %d after draining one page", chunksBefore, chunksAfter)
	}
	if after.ModelID != before.ModelID || after.TokenizerVersion != before.TokenizerVersion {
		t.Fatalf("the drain restamped the build identity %#v -> %#v", before, after)
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		status, err := svc.PageStatus(id)
		if err != nil {
			t.Fatalf("PageStatus(%s): %v", id, err)
		}
		if status.State != PageIndexed {
			t.Fatalf("%s = %q after draining another page, want indexed", id, status.State)
		}
	}
	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("queue holds %d pages after the drain, %v", count, err)
	}
}

func TestDrain_OneFailingPageLeavesTheBatchRunning(t *testing.T) {
	svc, store, _, pages := newTestService(t)

	if _, err := svc.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	before, err := store.CurrentBuild()
	if err != nil || before == nil {
		t.Fatalf("CurrentBuild = %#v, %v", before, err)
	}

	pages.pages["p1"] = &tree.Page{Content: "## 部署流程\n\nrun deploy again\n\n## Rollback\n\ngo back again"}
	pages.pages["p2"].Content = "edited too"
	for _, id := range []string{"p1", "p2"} {
		if err := svc.Enqueue(id); err != nil {
			t.Fatalf("Enqueue(%s): %v", id, err)
		}
	}
	// The second queued page disappears before the drain can read it.
	delete(pages.pages, "p2")

	if _, err := svc.Drain(0); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if status, err := svc.PageStatus("p1"); err != nil || status.State != PageIndexed {
		t.Fatalf("the healthy page = %#v, %v; want indexed", status, err)
	}
	if status, err := svc.PageStatus("p2"); err != nil || status.State != PageFailed {
		t.Fatalf("the unreadable page = %#v, %v; want failed", status, err)
	}
	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("queue still holds %d pages, %v", count, err)
	}
	if status, err := svc.Status(true); err != nil || status.State == StateFailed {
		t.Fatalf("one bad page marked the whole index %#v, %v", status, err)
	}
}

func TestDrain_NothingEmbeddedReportsFailure(t *testing.T) {
	svc, store, _, pages := newTestService(t)

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	before, err := store.CurrentBuild()
	if err != nil || before == nil {
		t.Fatalf("CurrentBuild = %#v, %v", before, err)
	}

	pages.pages["p2"].Content = "edited"
	if err := svc.Enqueue("p2"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	delete(pages.pages, "p2")

	if _, err := svc.Drain(0); err == nil {
		t.Fatal("a drain that embedded nothing reported success")
	}
	if count, err := store.ChunkCount(before.ID); err != nil || count != 2 {
		t.Fatalf("the answering build holds %d chunks, %v; want p1's 2", count, err)
	}
	if status, err := svc.Status(true); err != nil || status.State != StateFailed {
		t.Fatalf("status = %#v, %v; want failed so a rebuild is asked for", status, err)
	}
}

func TestPageStatus_EmbeddingAgainClearsAnEarlierFailure(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if err := svc.Enqueue("p1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := svc.store.MarkQueuedFailure("p1", "boom"); err != nil {
		t.Fatalf("MarkQueuedFailure: %v", err)
	}
	if status, err := svc.PageStatus("p1"); err != nil || status.State != PageFailed {
		t.Fatalf("status = %#v, %v; want the recorded failure", status, err)
	}

	// A single-page rebuild and a full rebuild both put the page back, so the
	// failure row must not outlive them: it would report a page as broken that
	// the index answers fine.
	if err := svc.IndexPage("p1"); err != nil {
		t.Fatalf("IndexPage: %v", err)
	}
	if status, err := svc.PageStatus("p1"); err != nil || status.State != PageIndexed {
		t.Fatalf("after IndexPage status = %#v, %v; want indexed", status, err)
	}

	if err := svc.Enqueue("p1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := svc.store.MarkQueuedFailure("p1", "boom"); err != nil {
		t.Fatalf("MarkQueuedFailure: %v", err)
	}
	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if status, err := svc.PageStatus("p1"); err != nil || status.State != PageIndexed {
		t.Fatalf("after a full rebuild status = %#v, %v; want indexed", status, err)
	}
}

func TestDrain_RebuildsWhenTheBuildIsAnotherModels(t *testing.T) {
	_, store, _, pages := newTestService(t)
	owner := NewService(store, &fakeEncoder{}, pages, "minilm", "wordpiece-1", ModeAuto, nil)
	if _, err := owner.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// A service configured for another model must not write its vectors into
	// the existing build: rebuilding is the only honest move (FR-408).
	other := NewService(store, &fakeEncoder{}, pages, "other-model", "other-tokenizer", ModeAuto, nil)
	if err := other.Enqueue("p1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := other.Drain(0); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v", current, err)
	}
	if current.ModelID != "other-model" || current.TokenizerVersion != "other-tokenizer" {
		t.Fatalf("build identity = %#v, want the draining service's", current)
	}
	for _, id := range []string{"p1", "p2"} {
		status, err := other.PageStatus(id)
		if err != nil || status.State != PageIndexed {
			t.Fatalf("%s = %#v, %v; want indexed by the full rebuild", id, status, err)
		}
	}
	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("queue holds %d pages, %v", count, err)
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
