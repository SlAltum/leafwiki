package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// fakeSyncEncoder embeds without a model directory, so the write path is
// reachable in a test.
type fakeSyncEncoder struct {
	calls int
	err   error
}

func (f *fakeSyncEncoder) Encode(texts []string) ([][]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = []float32{float32(len(text)), 1}
	}
	return out, nil
}

// syncOnce runs the command and returns its parsed JSON report.
func syncOnce(t *testing.T, cfg *serverConfig, opts syncOptions) (syncReport, error) {
	t.Helper()

	var out bytes.Buffer
	opts.asJSON = true
	err := runVectorsSync(cfg, opts, &out)
	if out.Len() == 0 {
		return syncReport{}, err
	}
	var report syncReport
	if jsonErr := json.Unmarshal(out.Bytes(), &report); jsonErr != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), jsonErr)
	}
	return report, err
}

// The property FR-165 sells: only the pages that drifted are embedded, and the
// first run builds the whole index instead of a partial one.
func TestRunVectorsSync_EmbedsOnlyTheDriftedPages(t *testing.T) {
	dataDir := t.TempDir()
	writeTestPages(t, dataDir)
	seedEmptyVectorsDB(t, dataDir)
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	encoder := &fakeSyncEncoder{}
	report, err := syncOnce(t, cfg, syncOptions{encoder: encoder})
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if report.Embedded != 3 {
		t.Fatalf("first run embedded %d pages, want the whole index: %#v", report.Embedded, report)
	}
	if report.State != string(vectors.StateFresh) {
		t.Fatalf("state = %q after the first build, want fresh", report.State)
	}

	// One page changes; the other two must stay as they are. Appending keeps the
	// frontmatter (and with it the leafwiki_id) that the first run wrote.
	appendToPage(t, filepath.Join(dataDir, "root", "alpha.md"), " and a second sentence")
	before := chunkCountOfTheBuild(t, dataDir)

	encoder = &fakeSyncEncoder{}
	report, err = syncOnce(t, cfg, syncOptions{encoder: encoder})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if report.Embedded != 1 || report.Stale != 1 {
		t.Fatalf("second run = %#v, want exactly the drifted page", report)
	}
	if report.Skipped != 2 {
		t.Fatalf("skipped = %d, want the two untouched pages", report.Skipped)
	}
	if after := chunkCountOfTheBuild(t, dataDir); after != before {
		t.Fatalf("chunk count %d -> %d; a one-page sync must not resize the index", before, after)
	}
}

// A page the wiki no longer has must lose its vectors (FR-165).
func TestRunVectorsSync_PurgesThePagesThatAreGone(t *testing.T) {
	dataDir := t.TempDir()
	ids := writeTestPages(t, dataDir)
	seedEmptyVectorsDB(t, dataDir)
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	if _, err := syncOnce(t, cfg, syncOptions{encoder: &fakeSyncEncoder{}}); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	addGhostChunks(t, dataDir, "a-page-that-is-gone")

	report, err := syncOnce(t, cfg, syncOptions{encoder: &fakeSyncEncoder{}})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if report.Purged != 1 {
		t.Fatalf("purged = %d, want the ghost page", report.Purged)
	}
	if left := chunkCountOfPage(t, dataDir, "a-page-that-is-gone"); left != 0 {
		t.Fatalf("the ghost page still holds %d chunks", left)
	}
	_ = ids
}

// A run that cannot embed has to say so through its exit code, not only through
// its report.
func TestRunVectorsSync_ReportsFailedPagesWithTheFailureCode(t *testing.T) {
	dataDir := t.TempDir()
	writeTestPages(t, dataDir)
	seedEmptyVectorsDB(t, dataDir)
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	encoder := &fakeSyncEncoder{err: errors.New("encoder exploded")}
	_, err := syncOnce(t, cfg, syncOptions{encoder: encoder})
	var coded *exitCodeError
	if !errors.As(err, &coded) || coded.code != exitCodeFailure {
		t.Fatalf("sync = %v, want exit code %d", err, exitCodeFailure)
	}
	if encoder.calls == 0 {
		t.Fatal("the encoder was never asked to embed")
	}
}

// --paths narrows the run and leaves the orphan purge alone.
func TestRunVectorsSync_RestrictsToTheGivenPaths(t *testing.T) {
	dataDir := t.TempDir()
	writeTestPages(t, dataDir)
	seedEmptyVectorsDB(t, dataDir)
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	if _, err := syncOnce(t, cfg, syncOptions{encoder: &fakeSyncEncoder{}}); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	for _, name := range []string{"alpha", "beta"} {
		appendToPage(t, filepath.Join(dataDir, "root", name+".md"), " and another sentence")
	}
	addGhostChunks(t, dataDir, "a-page-that-is-gone")

	report, err := syncOnce(t, cfg, syncOptions{encoder: &fakeSyncEncoder{}, paths: []string{"alpha"}})
	if err != nil {
		t.Fatalf("restricted sync: %v", err)
	}
	if report.Embedded != 1 {
		t.Fatalf("embedded = %d, want the one page in --paths", report.Embedded)
	}
	if report.Purged != 0 {
		t.Fatalf("purged = %d, want no purge on a restricted run", report.Purged)
	}
	if report.Scanned != 3 {
		t.Fatalf("scanned = %d, want the whole tree", report.Scanned)
	}
}

// A mistyped --data-dir must not be created and reported as success.
func TestRunVectorsSync_MissingDataDirIsNotFound(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "does-not-exist")

	err := runVectorsSync(&serverConfig{server: serverOptions{dataDir: dataDir}}, syncOptions{dryRun: true}, &bytes.Buffer{})
	var coded *exitCodeError
	if !errors.As(err, &coded) || coded.code != exitCodeNotFound {
		t.Fatalf("sync = %v, want exit code %d", err, exitCodeNotFound)
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the command created %s", dataDir)
	}
}

// seedEmptyVectorsDB creates the index database without a build: what a failed
// full build leaves behind, and the state a first sync has to repair.
func seedEmptyVectorsDB(t *testing.T, dataDir string) {
	t.Helper()

	store, err := embed.OpenStore(filepath.Join(dataDir, "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

// chunkCountOfTheBuild reports how many passages the current build holds.
func chunkCountOfTheBuild(t *testing.T, dataDir string) int {
	t.Helper()

	store, err := embed.OpenStore(filepath.Join(dataDir, "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("close store: %v", closeErr)
		}
	}()

	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v", current, err)
	}
	count, err := store.ChunkCount(current.ID)
	if err != nil {
		t.Fatalf("ChunkCount: %v", err)
	}
	return count
}

// addGhostChunks puts vectors of a page the wiki does not have into the build.
func addGhostChunks(t *testing.T, dataDir, pageID string) {
	t.Helper()

	store, err := embed.OpenStore(filepath.Join(dataDir, "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("close store: %v", closeErr)
		}
	}()

	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v", current, err)
	}
	if err := store.ReplacePageChunks(current.ID, pageID, []embed.Chunk{
		{PageID: pageID, ChunkNo: 0, ContentHash: "ghost", Vec: []float32{1}},
	}); err != nil {
		t.Fatalf("ReplacePageChunks: %v", err)
	}
}

// appendToPage adds text to a page while keeping its frontmatter.
func appendToPage(t *testing.T, path, text string) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close %s: %v", path, closeErr)
		}
	}()
	if _, err := file.WriteString(text); err != nil {
		t.Fatalf("append to %s: %v", path, err)
	}
}

// chunkCountOfPage reports the passages one page contributed to the build.
func chunkCountOfPage(t *testing.T, dataDir, pageID string) int {
	t.Helper()

	store, err := embed.OpenStore(filepath.Join(dataDir, "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("close store: %v", closeErr)
		}
	}()
	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild = %#v, %v", current, err)
	}
	chunks, err := store.PageChunks(current.ID, pageID)
	if err != nil {
		t.Fatalf("PageChunks: %v", err)
	}
	return len(chunks)
}
