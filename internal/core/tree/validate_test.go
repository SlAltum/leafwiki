package tree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// writeTreeFile writes one file below <dataDir>/root.
func writeTreeFile(t *testing.T, dataDir, relPath, content string) {
	t.Helper()

	path := filepath.Join(dataDir, "root", filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func problemsOfKind(report ValidationReport, kind ProblemKind) []Problem {
	var found []Problem
	for _, problem := range report.Problems {
		if problem.Kind == kind {
			found = append(found, problem)
		}
	}
	return found
}

func TestValidateTree_CleanTreeHasNoProblems(t *testing.T) {
	dataDir := t.TempDir()
	writeTreeFile(t, dataDir, "alpha.md", "# Alpha body")
	writeTreeFile(t, dataDir, "docs/index.md", "# Docs section")
	writeTreeFile(t, dataDir, "docs/child.md", "# Child body")

	report, err := ValidateTree(dataDir)
	if err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}
	if len(report.Problems) != 0 {
		t.Fatalf("problems = %#v, want none", report.Problems)
	}
	if report.Pages != 3 {
		t.Fatalf("pages = %d, want the two leaf pages and the section", report.Pages)
	}
	if report.HasErrors() || report.HasWarnings() {
		t.Fatal("a clean tree reported problems")
	}
}

func TestValidateTree_ReportsEachRule(t *testing.T) {
	dataDir := t.TempDir()

	// invalid filename: not a slug
	writeTreeFile(t, dataDir, "bad_name.md", "# Bad body")
	// name conflict: a directory and a .md file with the same name
	writeTreeFile(t, dataDir, "docs.md", "# Docs file")
	writeTreeFile(t, dataDir, "docs/index.md", "# Docs section")
	// duplicate leafwiki_id
	writeTreeFile(t, dataDir, "first.md", `---
leafwiki_id: shared-id
leafwiki_title: First
---
body`)
	writeTreeFile(t, dataDir, "second.md", `---
leafwiki_id: shared-id
leafwiki_title: Second
---
body`)
	// frontmatter that needs the repair path
	writeTreeFile(t, dataDir, "third.md", `---
leafwiki_title: ADR-0001: Filesystem as Source of Truth
---
body`)
	// sections without an index.md: the nested one and its parent
	writeTreeFile(t, dataDir, "empty/sec/child.md", "# Child body")

	report, err := ValidateTree(dataDir)
	if err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}

	if found := problemsOfKind(report, ProblemInvalidFilename); len(found) != 1 || found[0].Severity != SeverityWarning || found[0].Path != "bad_name.md" {
		t.Fatalf("invalid filename findings = %#v", found)
	}
	if found := problemsOfKind(report, ProblemNameConflict); len(found) != 1 || found[0].Severity != SeverityError {
		t.Fatalf("name conflict findings = %#v", found)
	}
	if found := problemsOfKind(report, ProblemDuplicateID); len(found) != 1 || found[0].Severity != SeverityError || !strings.Contains(found[0].Detail, "shared-id") {
		t.Fatalf("duplicate id findings = %#v", found)
	}
	if found := problemsOfKind(report, ProblemRepairedFrontmatter); len(found) != 1 || found[0].Severity != SeverityWarning {
		t.Fatalf("repaired frontmatter findings = %#v", found)
	}
	missing := problemsOfKind(report, ProblemMissingSectionIndex)
	if len(missing) != 2 {
		t.Fatalf("missing section index findings = %#v, want the empty section and its parent", missing)
	}
	for _, problem := range missing {
		if problem.Severity != SeverityWarning {
			t.Fatalf("%s = %q, want a warning", problem.Path, problem.Severity)
		}
	}
	if !report.HasErrors() {
		t.Fatal("errors were reported but HasErrors said no")
	}
}

func TestValidateTree_ReportsIgnoredContent(t *testing.T) {
	dataDir := t.TempDir()
	writeTreeFile(t, dataDir, ".leafwikiignore", "secret.md")
	writeTreeFile(t, dataDir, "secret.md", "# Secret body")
	writeTreeFile(t, dataDir, "open.md", "# Open body")

	report, err := ValidateTree(dataDir)
	if err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}
	if report.Ignored != 1 {
		t.Fatalf("ignored = %d, want the one excluded file", report.Ignored)
	}
	found := problemsOfKind(report, ProblemIgnored)
	if len(found) != 1 || found[0].Severity != SeverityInfo || found[0].Path != "secret.md" {
		t.Fatalf("ignored findings = %#v", found)
	}
	if report.Pages != 1 {
		t.Fatalf("pages = %d, want the visible page only", report.Pages)
	}
}

// FR-181: the check writes nothing.
func TestValidateTree_DoesNotTouchTheTree(t *testing.T) {
	dataDir := t.TempDir()
	writeTreeFile(t, dataDir, "alpha.md", "# Alpha body")
	writeTreeFile(t, dataDir, "docs/index.md", "# Docs body")
	writeTreeFile(t, dataDir, "broken_name.md", "# Bad body")

	before := hashTree(t, dataDir)
	if _, err := ValidateTree(dataDir); err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}
	if after := hashTree(t, dataDir); before != after {
		t.Fatalf("the check changed the tree: before %s, after %s", before, after)
	}
}

// hashTree fingerprints every file below dataDir, names, mtimes and content.
func hashTree(t *testing.T, dataDir string) string {
	t.Helper()

	sum := sha256.New()
	var paths []string
	if err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			t.Fatalf("relative path: %v", err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		_, _ = io.WriteString(sum, filepath.ToSlash(rel)+"|"+info.ModTime().UTC().String()+"|")
		_, _ = sum.Write(content)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// recordingHandler collects the records a reconstruction logs.
type recordingHandler struct {
	mu      sync.Mutex
	entries []string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	line := record.Message
	record.Attrs(func(attr slog.Attr) bool {
		line += " " + attr.Key + "=" + attr.Value.String()
		return true
	})
	h.entries = append(h.entries, line)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// findings returns the reconstruction's reasons in the check's vocabulary.
func (h *recordingHandler) findings() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	var kinds []string
	for _, entry := range h.entries {
		switch {
		case strings.Contains(entry, "invalid slug"):
			kinds = append(kinds, string(ProblemInvalidFilename))
		case strings.Contains(entry, "duplicate leafwiki_id"):
			kinds = append(kinds, string(ProblemDuplicateID))
		case strings.Contains(entry, "duplicate slug"):
			kinds = append(kinds, string(ProblemNameConflict))
		case strings.Contains(entry, "auto-repaired"):
			kinds = append(kinds, string(ProblemRepairedFrontmatter))
		case strings.Contains(entry, "could not load"):
			// The boot skips a file it cannot load; whether the cause is I/O or
			// the YAML is what the check refines.
			kinds = append(kinds, "load_failure")
		}
	}
	sort.Strings(kinds)
	return kinds
}

// unmapped returns the skip reasons the comparison above does not know about: a
// reason added to the boot without a counterpart in the check fails the parity
// test instead of slipping through.
func (h *recordingHandler) unmapped() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	known := []string{"invalid slug", "duplicate leafwiki_id", "duplicate slug", "auto-repaired", "could not load"}
	var lines []string
	for _, entry := range h.entries {
		if !strings.Contains(entry, "skipping") && !strings.Contains(entry, "could not ") {
			continue
		}
		matched := false
		for _, marker := range known {
			if strings.Contains(entry, marker) {
				matched = true
				break
			}
		}
		if !matched {
			lines = append(lines, entry)
		}
	}
	return lines
}

// FR-184b: the check's findings are the reasons a boot skips content. A rule
// added to one side without the other fails here.
func TestValidateTree_MatchesReconstructionFindings(t *testing.T) {
	dataDir := t.TempDir()
	writeTreeFile(t, dataDir, "bad_name.md", "# Bad body")
	writeTreeFile(t, dataDir, "first.md", `---
leafwiki_id: shared-id
leafwiki_title: First
---
body`)
	writeTreeFile(t, dataDir, "second.md", `---
leafwiki_id: shared-id
leafwiki_title: Second
---
body`)
	writeTreeFile(t, dataDir, "third.md", `---
leafwiki_title: ADR-0001: Filesystem as Source of Truth
---
body`)
	writeTreeFile(t, dataDir, "unparsable.md", `---
leafwiki_id: [invalid: yaml: structure
---
body`)

	// The check runs first: reconstruction repairs the fixture as it goes.
	report, err := ValidateTree(dataDir)
	if err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}

	recorder := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(recorder))
	defer slog.SetDefault(previous)

	store := NewNodeStore(dataDir)
	if _, err := store.ReconstructTreeFromFS(); err != nil {
		t.Fatalf("ReconstructTreeFromFS: %v", err)
	}

	if unmapped := recorder.unmapped(); len(unmapped) > 0 {
		t.Fatalf("reconstruction reported reasons the check does not model: %v", unmapped)
	}

	got := recorder.findings()
	var want []string
	for _, problem := range report.Problems {
		if problem.Severity == SeverityInfo {
			continue
		}
		// Both kinds describe "the boot skips a file it cannot load".
		if problem.Kind == ProblemUnreadableFile || problem.Kind == ProblemUnparsableFrontmatter {
			want = append(want, "load_failure")
			continue
		}
		want = append(want, string(problem.Kind))
	}
	sort.Strings(want)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("reconstruction findings = %v, check findings = %v", got, want)
	}
}

// The conflict that stops a boot must be an error on both sides.
func TestValidateTree_NameConflictMatchesTheBootFailure(t *testing.T) {
	dataDir := t.TempDir()
	writeTreeFile(t, dataDir, "docs.md", "# Docs file")
	writeTreeFile(t, dataDir, "docs/index.md", "# Docs section")

	report, err := ValidateTree(dataDir)
	if err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}
	if !report.HasErrors() {
		t.Fatal("a directory and a .md file with the same name is not reported as an error")
	}

	store := NewNodeStore(dataDir)
	if _, err := store.ReconstructTreeFromFS(); err == nil {
		t.Fatal("reconstruction accepted the name conflict the check calls an error")
	}
}
func TestValidateTree_ReservedRootIDIsADuplicate(t *testing.T) {
	dataDir := t.TempDir()
	writeTreeFile(t, dataDir, "page.md", `---
leafwiki_id: root
leafwiki_title: Page
---
body`)

	report, err := ValidateTree(dataDir)
	if err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}
	// The boot reserves this id for the root node, so it skips the page.
	found := problemsOfKind(report, ProblemDuplicateID)
	if len(found) != 1 || found[0].Severity != SeverityError {
		t.Fatalf("duplicate findings = %#v, want the reserved root id", found)
	}
}

// A file the boot cannot load never registers its name, so it cannot conflict
// with a directory of the same name.
func TestValidateTree_UnloadableFileIsNotAConflict(t *testing.T) {
	dataDir := t.TempDir()
	writeTreeFile(t, dataDir, "docs/index.md", "# Docs section")
	writeTreeFile(t, dataDir, "docs.md", `---
leafwiki_id: [invalid: yaml: structure
---
body`)

	report, err := ValidateTree(dataDir)
	if err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}
	if found := problemsOfKind(report, ProblemNameConflict); len(found) != 0 {
		t.Fatalf("conflict findings = %#v, want none for a file the boot skips", found)
	}
	if report.HasErrors() {
		t.Fatalf("problems = %#v, want no error", report.Problems)
	}
	if found := problemsOfKind(report, ProblemUnparsableFrontmatter); len(found) != 1 {
		t.Fatalf("frontmatter findings = %#v, want the unparsable file", found)
	}
}

// FR-182 names "frontmatter does not parse" as its own category, so a genuine
// parse failure must not be reported as an unreadable file.
func TestValidateTree_GenuinelyUnparsableFrontmatterHasItsOwnKind(t *testing.T) {
	dataDir := t.TempDir()
	writeTreeFile(t, dataDir, "page.md", `---
leafwiki_id: [invalid: yaml: structure
---
body`)

	report, err := ValidateTree(dataDir)
	if err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}
	if found := problemsOfKind(report, ProblemUnreadableFile); len(found) != 0 {
		t.Fatalf("unreadable findings = %#v, want none", found)
	}
	found := problemsOfKind(report, ProblemUnparsableFrontmatter)
	if len(found) != 1 || found[0].Severity != SeverityWarning || found[0].Path != "page.md" {
		t.Fatalf("frontmatter findings = %#v", found)
	}
}

// The page count must be the number of nodes the boot creates, not the number of
// entries on disk.
func TestValidateTree_PageCountMatchesTheBoot(t *testing.T) {
	dataDir := t.TempDir()
	writeTreeFile(t, dataDir, "alpha.md", "# Alpha body")
	writeTreeFile(t, dataDir, "bad_name.md", "# Skipped by the boot")
	writeTreeFile(t, dataDir, "docs/index.md", "# Docs section")
	writeTreeFile(t, dataDir, "docs/first.md", `---
leafwiki_id: twice
leafwiki_title: First
---
body`)
	writeTreeFile(t, dataDir, "docs/second.md", `---
leafwiki_id: twice
leafwiki_title: Second
---
body`)

	report, err := ValidateTree(dataDir)
	if err != nil {
		t.Fatalf("ValidateTree: %v", err)
	}

	store := NewNodeStore(dataDir)
	root, err := store.ReconstructTreeFromFS()
	if err != nil {
		t.Fatalf("ReconstructTreeFromFS: %v", err)
	}
	if got, want := report.Pages, countNodes(root); got != want {
		t.Fatalf("pages = %d, the boot created %d nodes", got, want)
	}
}

// countNodes counts the children below one node.
func countNodes(node *PageNode) int {
	if node == nil {
		return 0
	}
	count := 0
	for _, child := range node.Children {
		count += 1 + countNodes(child)
	}
	return count
}
