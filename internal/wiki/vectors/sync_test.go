package vectors

import (
	"testing"

	"github.com/perber/wiki/internal/core/tree"
)

func TestSyncPages_KeepsThePagesItWasNotGiven(t *testing.T) {
	svc, store, _, pages := newTestService(t)
	if _, err := svc.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	before, err := store.CurrentBuild()
	if err != nil || before == nil {
		t.Fatalf("CurrentBuild = %#v, %v", before, err)
	}

	pages.pages["p1"] = &tree.Page{Content: "## 部署流程\n\nrun deploy again\n\n## Rollback\n\ngo back again"}
	result, err := svc.SyncPages([]string{"p1"})
	if err != nil {
		t.Fatalf("SyncPages: %v", err)
	}
	if result.Pages != 1 || result.Chunks != 2 || len(result.Failed) != 0 {
		t.Fatalf("result = %#v, want one page with two passages", result)
	}

	after, err := store.CurrentBuild()
	if err != nil || after == nil {
		t.Fatalf("CurrentBuild = %#v, %v", after, err)
	}
	if after.ID != before.ID {
		t.Fatalf("a sync switched the generation %q -> %q", before.ID, after.ID)
	}
	if status, err := svc.PageStatus("p1"); err != nil || status.State != PageIndexed {
		t.Fatalf("the synced page = %#v, %v; want indexed", status, err)
	}
	if status, err := svc.PageStatus("p2"); err != nil || status.State != PageIndexed {
		t.Fatalf("the untouched page = %#v, %v; want indexed", status, err)
	}
}

// A batch may only open the first build when it covers the whole wiki: a
// generation holding one page while the rest are absent would call itself fresh
// (FR-164).
func TestSyncPages_FirstBuildCoversEveryPage(t *testing.T) {
	svc, store, encoder, _ := newTestService(t)

	result, err := svc.SyncPages([]string{"p1"})
	if err != nil {
		t.Fatalf("SyncPages: %v", err)
	}
	if result.Pages != 1 {
		t.Fatalf("result = %#v, want the one page that was asked for", result)
	}
	if calls := encoder.callCount(); calls != 2 {
		t.Fatalf("encoder ran %d times, want once per page of the first build", calls)
	}
	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v; want the batch to open the first build", current, err)
	}
	if count, err := store.ChunkCount(current.ID); err != nil || count != 3 {
		t.Fatalf("ChunkCount = %d, %v; want both pages' passages, not only the asked-for one", count, err)
	}
	if status, err := svc.PageStatus("p2"); err != nil || status.State != PageIndexed {
		t.Fatalf("the page nobody asked for = %#v, %v; want indexed", status, err)
	}
}

func TestSyncPages_ReportsPagesItCouldNotRead(t *testing.T) {
	svc, _, _, pages := newTestService(t)
	if _, err := svc.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	delete(pages.pages, "p2")

	result, err := svc.SyncPages([]string{"p1", "p2"})
	if err != nil {
		t.Fatalf("SyncPages: %v", err)
	}
	if result.Pages != 1 {
		t.Fatalf("result = %#v, want the readable page only", result)
	}
	if len(result.Failed) != 1 || result.Failed[0] != "p2" {
		t.Fatalf("failed = %v, want p2", result.Failed)
	}
}

func TestDiffPages_WorksWithoutAnEncoder(t *testing.T) {
	svc, store, _, pages := newTestService(t)
	if _, err := svc.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// The same store, read by a service that has no encoder: an instance without
	// a model directory still answers per-page states (FR-165b).
	reader := NewService(store, nil, pages, "minilm", "wordpiece-1", ModeManual, nil)
	if _, err := reader.PageStatus("p1"); err == nil {
		t.Fatal("PageStatus answered without an encoder, want the documented refusal")
	}

	status, err := reader.PageDiffStatus("p1")
	if err != nil {
		t.Fatalf("PageDiffStatus: %v", err)
	}
	if status.State != PageIndexed {
		t.Fatalf("state = %q, want indexed", status.State)
	}

	pages.pages["p2"].Content = "edited after the build"
	pages.pages["p3"] = &tree.Page{Content: "## 新页\n\nnot indexed yet"}
	pages.order = append(pages.order, "p3")

	diffs, err := reader.DiffPages()
	if err != nil {
		t.Fatalf("DiffPages: %v", err)
	}
	states := map[string]PageState{}
	for _, diff := range diffs {
		states[diff.PageID] = diff.State
	}
	if states["p1"] != PageIndexed {
		t.Fatalf("p1 = %q, want indexed", states["p1"])
	}
	if states["p2"] != PageStale {
		t.Fatalf("p2 = %q, want stale", states["p2"])
	}
	if states["p3"] != PageMissing {
		t.Fatalf("p3 = %q, want missing", states["p3"])
	}
}

func TestDiffPages_WithoutABuildReportsOff(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	diffs, err := svc.DiffPages()
	if err != nil {
		t.Fatalf("DiffPages: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("DiffPages = %#v, want every page", diffs)
	}
	for _, diff := range diffs {
		if diff.State != PageStateOff {
			t.Fatalf("%s = %q, want off without a build", diff.PageID, diff.State)
		}
	}
}

func TestOrphans_ListsIndexedPagesThatAreGone(t *testing.T) {
	svc, store, _, pages := newTestService(t)
	if _, err := svc.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v", current, err)
	}

	// p2 is deleted from disk, so nothing of it is left to keep in the index.
	delete(pages.pages, "p2")
	pages.order = []string{"p1"}

	orphans, err := svc.Orphans(current.ID)
	if err != nil {
		t.Fatalf("Orphans: %v", err)
	}
	if len(orphans) != 1 || orphans[0] != "p2" {
		t.Fatalf("orphans = %v, want p2", orphans)
	}
}
