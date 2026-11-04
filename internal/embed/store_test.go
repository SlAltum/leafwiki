package embed

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/perber/wiki/internal/core/shared/sqliteutil"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func TestStore_PageIDsListsEachPageOnce(t *testing.T) {
	store := openTestStore(t)

	build, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	other, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(build, []Chunk{
		{PageID: "a", ChunkNo: 0, ContentHash: "h", Vec: []float32{1}},
		{PageID: "a", ChunkNo: 1, ContentHash: "h", Vec: []float32{1}},
		{PageID: "b", ChunkNo: 0, ContentHash: "h", Vec: []float32{1}},
	}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}
	if err := store.PutChunks(other, []Chunk{
		{PageID: "only-in-the-other-build", ChunkNo: 0, ContentHash: "h", Vec: []float32{1}},
	}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}

	pageIDs, err := store.PageIDs(build)
	if err != nil {
		t.Fatalf("PageIDs: %v", err)
	}
	if len(pageIDs) != 2 || pageIDs[0] != "a" || pageIDs[1] != "b" {
		t.Fatalf("PageIDs = %v, want one entry per page of this build", pageIDs)
	}
}

func TestStore_RemoveQueuedPageOnlyDropsTheReadEntry(t *testing.T) {
	store := openTestStore(t)

	if err := store.EnqueuePage("p1", ""); err != nil {
		t.Fatalf("EnqueuePage: %v", err)
	}
	entries, err := store.QueuedPages(0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("QueuedPages = %#v, %v", entries, err)
	}

	// A save that landed after the entry was read re-pointed the row; the
	// embedding of the older content must not clear it.
	if err := store.RemoveQueuedPage("p1", entries[0].QueuedAt.Add(-time.Minute)); err != nil {
		t.Fatalf("RemoveQueuedPage: %v", err)
	}
	if count, err := store.QueueCount(); err != nil || count != 1 {
		t.Fatalf("queue holds %d pages, %v; want the newer entry kept", count, err)
	}

	if err := store.RemoveQueuedPage("p1", entries[0].QueuedAt); err != nil {
		t.Fatalf("RemoveQueuedPage: %v", err)
	}
	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("queue holds %d pages, %v; want the embedded entry gone", count, err)
	}
}

func TestOpenStore_CreatesSchema(t *testing.T) {
	store := openTestStore(t)

	if err := store.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	version, err := store.Meta(MetaSchemaVersion)
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %q, want %q", version, schemaVersion)
	}

	for _, table := range []string{"chunks", "meta", "queue"} {
		var name string
		err := store.withDBRead(func(db *sql.DB) error {
			return db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		})
		if err != nil {
			t.Fatalf("table %q missing: %v", table, err)
		}
	}
}

func TestStore_BuildSwitching(t *testing.T) {
	store := openTestStore(t)

	current, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("CurrentBuild: %v", err)
	}
	if current != nil {
		t.Fatalf("a fresh store reports build %#v, want none", current)
	}

	first, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(first, []Chunk{{PageID: "a", ChunkNo: 0, ContentHash: "h", Vec: []float32{1, 2}}}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}
	if err := store.CommitBuild(first, "minilm", "wordpiece-1"); err != nil {
		t.Fatalf("CommitBuild: %v", err)
	}

	// An interrupted second build must not disturb the first.
	second, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(second, []Chunk{{PageID: "b", ChunkNo: 0, ContentHash: "h", Vec: []float32{1}}}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}
	current, err = store.CurrentBuild()
	if err != nil {
		t.Fatalf("CurrentBuild: %v", err)
	}
	if current == nil || current.ID != first {
		t.Fatalf("CurrentBuild = %#v, want the first build %q", current, first)
	}
	if count, err := store.ChunkCount(first); err != nil || count != 1 {
		t.Fatalf("ChunkCount(first) = %d, %v", count, err)
	}

	// Committing the second build switches the pointer and drops the first.
	if err := store.CommitBuild(second, "minilm", "wordpiece-2"); err != nil {
		t.Fatalf("CommitBuild: %v", err)
	}
	current, err = store.CurrentBuild()
	if err != nil {
		t.Fatalf("CurrentBuild: %v", err)
	}
	if current == nil || current.ID != second || current.ModelID != "minilm" || current.TokenizerVersion != "wordpiece-2" {
		t.Fatalf("CurrentBuild = %#v", current)
	}
	if count, err := store.ChunkCount(first); err != nil || count != 0 {
		t.Fatalf("ChunkCount(first) = %d, %v; want the old generation dropped", count, err)
	}
	if count, err := store.ChunkCount(second); err != nil || count != 1 {
		t.Fatalf("ChunkCount(second) = %d, %v", count, err)
	}

	// A failed build can be cleaned up without touching the current one.
	third, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.DropBuild(third); err != nil {
		t.Fatalf("DropBuild: %v", err)
	}
	if current, err = store.CurrentBuild(); err != nil || current == nil || current.ID != second {
		t.Fatalf("CurrentBuild after dropping a failed build = %#v, %v", current, err)
	}
}

func TestStore_PutChunksNormalizesVectors(t *testing.T) {
	store := openTestStore(t)

	build, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(build, []Chunk{
		{PageID: "a", ChunkNo: 0, Heading: "h", HeadingPath: "h", Anchor: "h", ContentHash: "hash", Vec: []float32{3, 4}},
		{PageID: "b", ChunkNo: 0, ContentHash: "hash", Vec: []float32{0, 0}},
	}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}

	read := func(pageID string) []float32 {
		t.Helper()
		var raw []byte
		err := store.withDBRead(func(db *sql.DB) error {
			return db.QueryRow(`SELECT vec FROM chunks WHERE build_id = ? AND page_id = ? AND chunk_no = 0`, build, pageID).Scan(&raw)
		})
		if err != nil {
			t.Fatalf("reading vec of %q: %v", pageID, err)
		}
		return decodeVec(raw)
	}

	got := read("a")
	if len(got) != 2 || got[0] < 0.599 || got[0] > 0.601 || got[1] < 0.799 || got[1] > 0.801 {
		t.Fatalf("stored vector = %v, want the unit vector [0.6 0.8]", got)
	}
	if zero := read("b"); len(zero) != 2 || zero[0] != 0 || zero[1] != 0 {
		t.Fatalf("zero vector came back as %v", zero)
	}
}

func TestStore_ResetForgetsTheConversion(t *testing.T) {
	store := openTestStore(t)

	build, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(build, []Chunk{{PageID: "a", ChunkNo: 0, ContentHash: "h", Vec: []float32{1}}}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}
	if err := store.EnqueuePage("b", build); err != nil {
		t.Fatalf("EnqueuePage: %v", err)
	}
	if err := store.CommitBuild(build, "minilm", "wordpiece-1"); err != nil {
		t.Fatalf("CommitBuild: %v", err)
	}
	if err := store.SetMeta(MetaPaused, "true"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}

	if err := store.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if current, err := store.CurrentBuild(); err != nil || current != nil {
		t.Fatalf("CurrentBuild after Reset = %#v, %v; want none", current, err)
	}
	if count, err := store.ChunkCount(build); err != nil || count != 0 {
		t.Fatalf("ChunkCount after Reset = %d, %v", count, err)
	}
	if count, err := store.QueueCount(); err != nil || count != 0 {
		t.Fatalf("QueueCount after Reset = %d, %v", count, err)
	}
	// The pause flag is an operator decision about indexing, not about content.
	if paused, err := store.Meta(MetaPaused); err != nil || paused != "true" {
		t.Fatalf("paused after Reset = %q, %v", paused, err)
	}
}

func TestOpenStore_CorruptFileIsReportedNotDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")
	if err := os.WriteFile(path, []byte("this is not a database"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store, err := OpenStore(path)
	if err == nil {
		_ = store.Close()
		t.Fatal("OpenStore accepted a corrupt database")
	}
	var corrupt *sqliteutil.CorruptDatabaseError
	if !errors.As(err, &corrupt) {
		t.Fatalf("OpenStore = %v, want a CorruptDatabaseError", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("the corrupt file was removed: %v", statErr)
	}
}
