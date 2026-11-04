package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/embed"
)

// writeTestPages puts three pages under <dataDir>/root and returns their ids as
// the tree assigns them.
func writeTestPages(t *testing.T, dataDir string) map[string]string {
	t.Helper()

	root := filepath.Join(dataDir, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	for name, content := range map[string]string{
		"alpha.md": "# Alpha first page",
		"beta.md":  "# Beta second page",
		"gamma.md": "# Gamma third page",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	treeService, err := loadVectorTree(dataDir)
	if err != nil {
		t.Fatalf("loadVectorTree: %v", err)
	}
	ids := map[string]string{}
	for _, slug := range []string{"alpha", "beta", "gamma"} {
		page, err := treeService.FindPageByRoutePath(slug)
		if err != nil || page == nil {
			t.Fatalf("FindPageByRoutePath(%s): %v", slug, err)
		}
		ids[slug] = page.ID
	}
	return ids
}

// A build that holds one page with an outdated passage and none of the others:
// the state a run has to repair.
func seedDriftedBuild(t *testing.T, dataDir, pageID string) {
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

	build, err := store.BeginBuild()
	if err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	if err := store.PutChunks(build, []embed.Chunk{
		{PageID: pageID, ChunkNo: 0, ContentHash: "an-old-hash", Vec: []float32{1}},
	}); err != nil {
		t.Fatalf("PutChunks: %v", err)
	}
	if err := store.CommitBuild(build, "", ""); err != nil {
		t.Fatalf("CommitBuild: %v", err)
	}
}

func TestRunVectorsSync_DryRunReportsDriftWithoutAModel(t *testing.T) {
	dataDir := t.TempDir()
	ids := writeTestPages(t, dataDir)
	seedDriftedBuild(t, dataDir, ids["alpha"])

	var out bytes.Buffer
	if err := runVectorsSync(&serverConfig{server: serverOptions{dataDir: dataDir}}, syncOptions{dryRun: true, asJSON: true}, &out); err != nil {
		t.Fatalf("runVectorsSync: %v", err)
	}

	var report syncReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if report.Scanned != 3 {
		t.Fatalf("scanned = %d, want the three pages", report.Scanned)
	}
	if report.Stale != 1 {
		t.Fatalf("stale = %d, want the drifted page", report.Stale)
	}
	if report.Missing != 2 {
		t.Fatalf("missing = %d, want the two never-embedded pages", report.Missing)
	}
	if report.Embedded != 0 || report.Purged != 0 {
		t.Fatalf("report = %#v, want a dry run to write nothing", report)
	}
}

func TestRunVectorsSync_WithoutADatabaseReportsOff(t *testing.T) {
	dataDir := t.TempDir()

	var out bytes.Buffer
	if err := runVectorsSync(&serverConfig{server: serverOptions{dataDir: dataDir}}, syncOptions{asJSON: true}, &out); err != nil {
		t.Fatalf("runVectorsSync: %v", err)
	}
	var report syncReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if report.State != "off" {
		t.Fatalf("state = %q, want off without a database", report.State)
	}
}

func TestRunVectorsSync_ReportsLockedWhenTheDataDirIsInUse(t *testing.T) {
	dataDir := t.TempDir()
	lock, err := acquireTestWikiLock(dataDir)
	if err != nil {
		t.Fatalf("acquireTestWikiLock: %v", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			t.Errorf("release: %v", releaseErr)
		}
	}()

	err = runVectorsSync(&serverConfig{server: serverOptions{dataDir: dataDir}}, syncOptions{dryRun: true}, &bytes.Buffer{})
	var coded *exitCodeError
	if !errors.As(err, &coded) {
		t.Fatalf("runVectorsSync = %v, want an exit code error", err)
	}
	if coded.code != exitCodeLocked {
		t.Fatalf("exit code = %d, want %d", coded.code, exitCodeLocked)
	}
}

func TestSplitPaths(t *testing.T) {
	if got := splitPaths("  "); got != nil {
		t.Fatalf("splitPaths(blank) = %v, want nil", got)
	}
	got := splitPaths(" a/b , c ,, d ")
	if len(got) != 3 || got[0] != "a/b" || got[1] != "c" || got[2] != "d" {
		t.Fatalf("splitPaths = %v", got)
	}
}
