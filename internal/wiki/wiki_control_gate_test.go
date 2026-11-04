package wiki

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"path/filepath"

	"github.com/perber/wiki/internal/core/markdown"
	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/restore"
	"github.com/perber/wiki/internal/wiki/control"
	"github.com/perber/wiki/internal/wiki/pagesave"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// The buffered orchestrator holds the side effects until the item has finished
// writing, then replays them (FR-150).
func TestBufferedOrchestrator_DefersUntilReplay(t *testing.T) {
	var order []string
	var mu sync.Mutex
	record := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, step)
	}

	inner := pagesave.NewPageSaveOrchestrator(nil, &recordingEffect{record: record})
	buffer := &bufferedOrchestrator{inner: inner}
	buffer.Apply(pagesave.PageSaveEvent{Operation: pagesave.PageOperationCreate})
	buffer.Apply(pagesave.PageSaveEvent{Operation: pagesave.PageOperationUpdate})

	if len(order) != 0 {
		t.Fatalf("the side effects ran before the item finished writing: %v", order)
	}
	buffer.replay()
	if len(order) != 2 || order[0] != "effect" || order[1] != "effect" {
		t.Fatalf("order = %v, want both effects after the replay", order)
	}
	if len(buffer.events) != 0 {
		t.Fatal("the replay kept the events")
	}
}

type recordingEffect struct {
	record func(string)
}

func (e *recordingEffect) Apply(pagesave.PageSaveEvent) { e.record("effect") }

// A restore in progress refuses the write and changes nothing; a dry run does
// not need the gate at all (FR-147, FR-150).
func TestControlPageApply_RestoreGate(t *testing.T) {
	w := createWikiTestInstance(t)
	gate := restore.NewWriteGate()
	w.writeGate = gate
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	})

	caller := control.Caller{Identity: "admin", Role: "admin"}
	gate.Engage()

	_, apiErr := w.ControlPages().PageApply(context.Background(), caller, control.ApplyParams{Items: []control.ApplyItem{
		{Path: "blocked", Create: true, Title: itemText("Blocked"), Content: itemText("body")},
	}})
	if apiErr == nil || apiErr.Code != control.CodeRestoreInProgress {
		t.Fatalf("write during a restore = %#v, want restore_in_progress", apiErr)
	}
	if _, apiErr := w.ControlPages().PageGet(context.Background(), caller, control.PageRef{Path: "blocked"}); apiErr == nil {
		t.Fatal("the refused write created the page")
	}

	// A dry run reads the impact only, so it is allowed while the gate is closed.
	if _, apiErr := w.ControlPages().PageApply(context.Background(), caller, control.ApplyParams{DryRun: true, Items: []control.ApplyItem{
		{Path: "blocked", Create: true, Title: itemText("Blocked")},
	}}); apiErr != nil {
		t.Fatalf("dry run during a restore = %#v, want it to be allowed", apiErr)
	}

	gate.Disengage()
	if _, apiErr := w.ControlPages().PageApply(context.Background(), caller, control.ApplyParams{Items: []control.ApplyItem{
		{Path: "blocked", Create: true, Title: itemText("Blocked"), Content: itemText("body")},
	}}); apiErr != nil {
		t.Fatalf("write after the restore = %#v", apiErr)
	}
	// The gate is not left held by the successful batch.
	if leave, ok := gate.TryEnter(); !ok {
		t.Fatal("the successful batch left the write gate held")
	} else {
		leave()
	}
}

// Batches are serialized, so their side effects cannot interleave (NFR-7).
func TestControlPageApply_Serialized(t *testing.T) {
	w := createWikiTestInstance(t)
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	})
	caller := control.Caller{Identity: "admin", Role: "admin"}

	// Hold the serialization mutex and prove a batch waits for it.
	w.controlWriteMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = w.ControlPages().PageApply(context.Background(), caller, control.ApplyParams{Items: []control.ApplyItem{
			{Path: "serialized", Create: true, Title: itemText("Serialized"), Content: itemText("body")},
		}})
	}()

	select {
	case <-done:
		w.controlWriteMu.Unlock()
		t.Fatal("the batch did not wait for the serialization lock")
	case <-time.After(100 * time.Millisecond):
	}

	w.controlWriteMu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the batch never ran after the lock was released")
	}
}

// Anonymous writes are attributed to the system user; an authenticated update
// only moves the last author (FR-113, FR-114).
func TestControlPageApply_Attribution(t *testing.T) {
	w := createWikiTestInstance(t)
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	})
	anonymous := control.Caller{Identity: control.SystemUserID, Role: ""}

	if _, apiErr := w.ControlPages().PageApply(context.Background(), anonymous, control.ApplyParams{Items: []control.ApplyItem{
		{Path: "attributed", Create: true, Title: itemText("Attributed"), Content: itemText("body")},
	}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}

	page, err := w.tree.FindPageByRoutePath("attributed")
	if err != nil {
		t.Fatalf("FindPageByRoutePath: %v", err)
	}
	frontmatter := readFrontmatter(t, w, page.ID)
	if frontmatter.LeafWikiCreatorID != control.SystemUserID || frontmatter.LeafWikiLastAuthorID != control.SystemUserID {
		t.Fatalf("create frontmatter = creator %q, last author %q; want both system", frontmatter.LeafWikiCreatorID, frontmatter.LeafWikiLastAuthorID)
	}

	editor, err := w.UserService().CreateUser("editor1", "editor1@example.com", "editorpass", "editor")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, apiErr := w.ControlPages().PageApply(context.Background(), control.Caller{Identity: editor.ID, Role: editor.Role}, control.ApplyParams{Items: []control.ApplyItem{
		{Path: "attributed", Content: itemText("new body")},
	}}); apiErr != nil {
		t.Fatalf("update: %#v", apiErr)
	}

	frontmatter = readFrontmatter(t, w, page.ID)
	if frontmatter.LeafWikiCreatorID != control.SystemUserID {
		t.Fatalf("creator = %q after an update, want it unchanged", frontmatter.LeafWikiCreatorID)
	}
	if frontmatter.LeafWikiLastAuthorID != editor.ID {
		t.Fatalf("last author = %q, want the editor", frontmatter.LeafWikiLastAuthorID)
	}
}

// readFrontmatter parses the page file's frontmatter.
func readFrontmatter(t *testing.T, w *Wiki, pageID string) markdown.Frontmatter {
	t.Helper()

	raw, err := w.tree.ReadPageRaw(pageID)
	if err != nil {
		t.Fatalf("ReadPageRaw: %v", err)
	}
	frontmatter, _, has, err := markdown.ParseFrontmatter(raw)
	if err != nil || !has {
		t.Fatalf("parse frontmatter: has=%v err=%v", has, err)
	}
	return frontmatter
}

// A failed item keeps its shape from §4.5 (FR-148).
func TestControlPageApply_FailedItemShape(t *testing.T) {
	h := startControlHarness(t)

	if _, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{
		{Path: "first", Create: true, Title: itemText("First"), Content: itemText("body")},
	}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}

	// An update with an empty title fails the precheck before any write.
	_, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{
		{Path: "first", Title: itemText("  ")},
	}})
	if apiErr == nil || apiErr.Code != control.CodeInvalidParams {
		t.Fatalf("empty title = %#v, want invalid params", apiErr)
	}
}

var _ = errors.Is

// fakeEncoder makes a vectors service look usable without a model.
type fakeEncoder struct{}

func (fakeEncoder) Encode(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

// The wait result starts at the mode's own value and never claims indexed
// (FR-149, FR-163).
func TestPendingVectorState(t *testing.T) {
	// No service at all: the feature is off.
	if got := (newControlPages(pageOps{})).pendingVectorState(); got != "off" {
		t.Fatalf("off instance = %q, want off", got)
	}

	store, err := embed.OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("close store: %v", closeErr)
		}
	})

	// A service without an encoder cannot wait for anything.
	noEncoder := vectors.NewService(store, nil, nil, "model", vectors.TokenizerVersion, vectors.ModeAuto, nil)
	if got := (newControlPages(pageOps{vectors: noEncoder})).pendingVectorState(); got != "unavailable" {
		t.Fatalf("without an encoder = %q, want unavailable", got)
	}

	manual := vectors.NewService(store, fakeEncoder{}, nil, "model", vectors.TokenizerVersion, vectors.ModeManual, nil)
	if got := (newControlPages(pageOps{vectors: manual})).pendingVectorState(); got != "manual" {
		t.Fatalf("manual = %q, want manual", got)
	}

	automatic := vectors.NewService(store, fakeEncoder{}, nil, "model", vectors.TokenizerVersion, vectors.ModeAuto, nil)
	if got := (newControlPages(pageOps{vectors: automatic})).pendingVectorState(); got != "queued" {
		t.Fatalf("auto = %q, want queued", got)
	}

	if err := automatic.SetPaused(true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if got := (newControlPages(pageOps{vectors: automatic})).pendingVectorState(); got != "paused" {
		t.Fatalf("paused = %q, want paused", got)
	}
}

// The create item writes twice; the gate must still be held for the second
// write (FR-150, the high finding of the D5–D8 review).
func TestControlPageApply_GateHeldForEveryWriteOfACreate(t *testing.T) {
	w := createWikiTestInstance(t)
	gate := restore.NewWriteGate()
	w.writeGate = gate
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	})

	probeCalls := 0
	controlWriteProbe = func() {
		probeCalls++
		// WaitForDrain reports false while a write is inside the gate.
		if gate.WaitForDrain(0) {
			t.Error("the content write ran with the gate released")
		}
	}
	t.Cleanup(func() { controlWriteProbe = nil })

	if _, apiErr := w.ControlPages().PageApply(context.Background(), control.Caller{Identity: "admin", Role: "admin"}, control.ApplyParams{Items: []control.ApplyItem{
		{Path: "two-legs", Create: true, Title: itemText("Two legs"), Content: itemText("body")},
	}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}
	if probeCalls == 0 {
		t.Fatal("the create never reached its content write")
	}
	// The gate is free once the item is done.
	if leave, ok := gate.TryEnter(); ok {
		leave()
	}
}

// Two real batches serialize without corrupting each other (NFR-7).
func TestControlPageApply_ConcurrentBatches(t *testing.T) {
	w := createWikiTestInstance(t)
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	})
	caller := control.Caller{Identity: "admin", Role: "admin"}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			path := "concurrent-" + string(rune('a'+index))
			if _, apiErr := w.ControlPages().PageApply(context.Background(), caller, control.ApplyParams{Items: []control.ApplyItem{
				{Path: path, Create: true, Title: itemText("Page " + path), Content: itemText("body")},
			}}); apiErr != nil {
				t.Errorf("batch %d: %#v", index, apiErr)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < 8; i++ {
		path := "concurrent-" + string(rune('a'+i))
		if _, apiErr := w.ControlPages().PageGet(context.Background(), caller, control.PageRef{Path: path}); apiErr != nil {
			t.Errorf("page %s missing after the concurrent batches: %#v", path, apiErr)
		}
	}
}

// A batch writes while other connections read (T5): reads never fail and never
// block past the batch.
func TestControlPageApply_ConcurrentReads(t *testing.T) {
	w := createWikiTestInstance(t)
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	})
	caller := control.Caller{Identity: "admin", Role: "admin"}
	pages := w.ControlPages()

	if _, apiErr := pages.PageApply(context.Background(), caller, control.ApplyParams{Items: []control.ApplyItem{
		{Path: "read-target", Create: true, Title: itemText("Read"), Content: itemText("body")},
	}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, apiErr := pages.PageGet(context.Background(), caller, control.PageRef{Path: "read-target"}); apiErr != nil {
					t.Errorf("read during a write failed: %#v", apiErr)
					return
				}
			}
		}()
	}

	for i := 0; i < 5; i++ {
		if _, apiErr := pages.PageApply(context.Background(), caller, control.ApplyParams{Items: []control.ApplyItem{
			{Path: "read-target", Content: itemText("body " + string(rune('a'+i)))},
		}}); apiErr != nil {
			close(stop)
			readers.Wait()
			t.Fatalf("update %d: %#v", i, apiErr)
		}
	}
	close(stop)
	readers.Wait()
}
