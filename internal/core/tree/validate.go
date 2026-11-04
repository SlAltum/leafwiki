package tree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/perber/wiki/internal/core/ignore"
	"github.com/perber/wiki/internal/core/markdown"
)

// Severity is how bad a check finding is. A warning is content the server
// tolerates, an error is content that costs a page or a start (FR-182).
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// ProblemKind is the stable name of one check finding.
type ProblemKind string

const (
	// ProblemInvalidFilename marks a page or section name that is not a valid
	// slug, which the server skips on boot.
	ProblemInvalidFilename ProblemKind = "invalid_filename"
	// ProblemNameConflict marks two entries that claim the same name, which
	// fails the boot.
	ProblemNameConflict ProblemKind = "name_conflict"
	// ProblemDuplicateID marks two files that carry the same leafwiki_id; the
	// server keeps the first and skips the rest.
	ProblemDuplicateID ProblemKind = "duplicate_id"
	// ProblemUnparsableFrontmatter marks a file whose frontmatter does not
	// parse; the server skips it.
	ProblemUnparsableFrontmatter ProblemKind = "unparsable_frontmatter"
	// ProblemRepairedFrontmatter marks a file whose frontmatter did not parse as
	// written and gets rewritten on the next start.
	ProblemRepairedFrontmatter ProblemKind = "repaired_frontmatter"
	// ProblemUnreadableFile marks a file the server could not load and skips.
	ProblemUnreadableFile ProblemKind = "unreadable_file"
	// ProblemUnreadableDirectory marks a directory the server cannot read,
	// which stops the boot.
	ProblemUnreadableDirectory ProblemKind = "unreadable_directory"
	// ProblemIgnored marks content the ignore rules keep out of the wiki.
	ProblemIgnored ProblemKind = "ignored"
	// ProblemMissingSectionIndex marks a section without an index.md, which the
	// next boot creates.
	ProblemMissingSectionIndex ProblemKind = "missing_section_index"
)

// Problem is one finding, shaped for the check command's output (FR-183).
type Problem struct {
	Kind     ProblemKind `json:"kind"`
	Severity Severity    `json:"severity"`
	Path     string      `json:"path"`
	Detail   string      `json:"detail"`
	Hint     string      `json:"hint,omitempty"`
}

// ValidationReport is what one read-only tree check found.
type ValidationReport struct {
	// Pages counts the nodes the server would create from disk.
	Pages int `json:"pages"`
	// Problems is sorted by path, so a run is reproducible.
	Problems []Problem `json:"problems"`
	// Ignored counts entries the ignore rules exclude.
	Ignored int `json:"ignored"`
}

// HasErrors reports whether anything would cost content or a start.
func (r ValidationReport) HasErrors() bool {
	for _, problem := range r.Problems {
		if problem.Severity == SeverityError {
			return true
		}
	}
	return false
}

// HasWarnings reports whether anything is content the server would drop.
func (r ValidationReport) HasWarnings() bool {
	for _, problem := range r.Problems {
		if problem.Severity == SeverityWarning {
			return true
		}
	}
	return false
}

// ValidateTree checks the page tree without touching it: no writes, no store, no
// lock (FR-181). It applies the rules the boot applies, so its findings are the
// reasons a boot would skip or refuse content (FR-182, FR-184b). A missing root
// directory is an empty wiki, which is what the boot makes of it.
func ValidateTree(storageDir string) (ValidationReport, error) {
	rootDir := filepath.Join(storageDir, "root")
	info, err := os.Stat(rootDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ValidationReport{Problems: []Problem{}}, nil
		}
		return ValidationReport{}, fmt.Errorf("check %s: %w", rootDir, err)
	}
	if !info.IsDir() {
		// The boot refuses a root that is not a directory, so the check says so
		// instead of falling over.
		return ValidationReport{Problems: []Problem{{
			Kind:     ProblemUnreadableDirectory,
			Severity: SeverityError,
			Path:     "root",
			Detail:   "the root path is not a directory",
			Hint:     "move the file aside so the server can create its root directory",
		}}}, nil
	}

	walker := &treeValidator{
		rootDir: rootDir,
		ignore:  ignore.NewCache(rootDir),
		// The boot reserves this id for the root node before it walks, so a page
		// carrying it is a duplicate there too.
		seenIDs:  map[string]string{"root": "."},
		problems: []Problem{},
	}
	if err := walker.walk(rootDir); err != nil {
		return ValidationReport{}, err
	}
	sort.SliceStable(walker.problems, func(i, j int) bool {
		if walker.problems[i].Path != walker.problems[j].Path {
			return walker.problems[i].Path < walker.problems[j].Path
		}
		return walker.problems[i].Kind < walker.problems[j].Kind
	})
	return ValidationReport{
		Pages:    walker.nodes,
		Problems: walker.problems,
		Ignored:  walker.ignored,
	}, nil
}

// treeValidator carries the state of one walk: what was counted, which ids were
// seen, and what has to be reported.
type treeValidator struct {
	rootDir  string
	ignore   *ignore.Cache
	slugger  *SlugService
	seenIDs  map[string]string
	problems []Problem
	nodes    int
	ignored  int
}

// walk visits one directory and fills the problems it finds.
func (v *treeValidator) walk(dir string) error {
	if v.slugger == nil {
		v.slugger = NewSlugService()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// The boot cannot read this directory either, and refuses to start.
		v.report(Problem{
			Kind:     ProblemUnreadableDirectory,
			Severity: SeverityError,
			Path:     v.relative(dir),
			Detail:   err.Error(),
			Hint:     "fix the permissions or remove the directory, otherwise the server does not start",
		})
		return nil
	}
	sortReconstructionEntries(entries)

	seenSlugs := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		fullPath := filepath.Join(dir, name)
		relPath := v.relative(fullPath)

		if rules := v.ignore.Get(dir); rules != nil && rules.Matches(relPath, entry.IsDir()) {
			v.ignored++
			v.report(Problem{
				Kind:     ProblemIgnored,
				Severity: SeverityInfo,
				Path:     relPath,
				Detail:   "excluded by .leafwikiignore",
				Hint:     "the server will not show this content",
			})
			continue
		}

		if entry.IsDir() {
			v.checkSection(name, fullPath, relPath, seenSlugs)
			continue
		}
		v.checkPage(name, fullPath, relPath, seenSlugs)
	}
	return nil
}

// checkSection validates one directory entry and recurses into it.
func (v *treeValidator) checkSection(name, fullPath, relPath string, seenSlugs map[string]string) {
	if err := v.slugger.IsValidSlug(name); err != nil {
		v.report(Problem{
			Kind:     ProblemInvalidFilename,
			Severity: SeverityWarning,
			Path:     relPath,
			Detail:   err.Error(),
			Hint:     "rename the directory to a slug the server accepts (letters, digits and hyphens), otherwise it is skipped on boot",
		})
		return
	}

	v.checkFrontmatter(filepath.Join(fullPath, indexFilename), relPath, true)

	// The boot records a section's name before it looks at its content, so the
	// conflict is real even when the section's index.md is unreadable.
	if !v.checkSlugConflict(seenSlugs, strings.ToLower(name), relPath) {
		return
	}
	if !v.checkDuplicateID(filepath.Join(fullPath, indexFilename), relPath) {
		return
	}
	v.nodes++

	if err := v.walk(fullPath); err != nil {
		return
	}
}

// checkPage validates one markdown file.
func (v *treeValidator) checkPage(name, fullPath, relPath string, seenSlugs map[string]string) {
	ext := filepath.Ext(name)
	if !strings.EqualFold(ext, ".md") {
		return
	}
	slug := strings.TrimSuffix(name, ext)
	if strings.EqualFold(slug, "index") {
		return
	}
	if err := v.slugger.IsValidSlug(slug); err != nil {
		v.report(Problem{
			Kind:     ProblemInvalidFilename,
			Severity: SeverityWarning,
			Path:     relPath,
			Detail:   err.Error(),
			Hint:     "rename the file to a slug the server accepts (letters, digits and hyphens), otherwise it is skipped on boot",
		})
		return
	}

	// The boot loads a page before it records its name, so a file it cannot load
	// is not part of a conflict.
	if !v.checkFrontmatter(fullPath, relPath, false) {
		return
	}
	if !v.checkSlugConflict(seenSlugs, strings.ToLower(slug), relPath) {
		return
	}
	if !v.checkDuplicateID(fullPath, relPath) {
		return
	}
	v.nodes++
}

// checkFrontmatter classifies one content file the way the boot loads it: an
// unreadable file or an unparsable frontmatter is skipped (a warning), content
// that needed the repair path is repaired (a warning), and a section without an
// index.md is created on the next start (a warning). tolerated says the caller's
// node survives a load failure, which is true for a section.
func (v *treeValidator) checkFrontmatter(filePath, relPath string, tolerated bool) bool {
	if _, err := os.Stat(filePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !tolerated {
				v.report(Problem{
					Kind:     ProblemUnreadableFile,
					Severity: SeverityWarning,
					Path:     relPath,
					Detail:   err.Error(),
				})
				return false
			}
			v.report(Problem{
				Kind:     ProblemMissingSectionIndex,
				Severity: SeverityWarning,
				Path:     relPath,
				Detail:   "the section has no index.md",
				Hint:     "the server creates it on the next start",
			})
			return false
		}
		// The boot stops on a section it cannot materialize and skips an
		// unreadable page, so this is an error only where it stops the start.
		v.report(Problem{
			Kind:     ProblemUnreadableFile,
			Severity: SeverityWarning,
			Path:     relPath,
			Detail:   err.Error(),
		})
		return false
	}

	raw, err := os.ReadFile(filePath)
	if err != nil {
		v.report(Problem{
			Kind:     ProblemUnreadableFile,
			Severity: SeverityWarning,
			Path:     relPath,
			Detail:   err.Error(),
			Hint:     "the server skips this file on boot",
		})
		return false
	}
	if _, _, has, parseErr := markdown.ParseFrontmatter(string(raw)); parseErr != nil {
		detail := parseErr.Error()
		hint := "the server skips this file on boot"
		if has {
			hint = "the server skips this file on boot; fix the YAML or delete the frontmatter block"
		}
		v.report(Problem{
			Kind:     ProblemUnparsableFrontmatter,
			Severity: SeverityWarning,
			Path:     relPath,
			Detail:   detail,
			Hint:     hint,
		})
		return false
	}

	mdFile, err := markdown.LoadMarkdownFile(filePath)
	if err != nil {
		v.report(Problem{
			Kind:     ProblemUnreadableFile,
			Severity: SeverityWarning,
			Path:     relPath,
			Detail:   err.Error(),
			Hint:     "the server skips this file on boot",
		})
		return false
	}
	if mdFile.GetFrontmatter().WasRepaired() {
		v.report(Problem{
			Kind:     ProblemRepairedFrontmatter,
			Severity: SeverityWarning,
			Path:     relPath,
			Detail:   "frontmatter did not parse as written and gets repaired",
		})
	}
	return true
}

// checkSlugConflict reports two entries that claim one name, which is the
// conflict that stops the boot (FR-182). It reports false when the entry is the
// second one.
func (v *treeValidator) checkSlugConflict(seen map[string]string, key, path string) bool {
	if existing, ok := seen[key]; ok {
		v.report(Problem{
			Kind:     ProblemNameConflict,
			Severity: SeverityError,
			Path:     path,
			Detail:   fmt.Sprintf("two entries share the name %q (the other one is %s)", key, existing),
			Hint:     "rename or remove one of them, for example " + key + "-page.md, otherwise the server does not start",
		})
		return false
	}
	seen[key] = path
	return true
}

// checkDuplicateID reports a file that carries a leafwiki_id another file
// already used. The boot keeps the first occurrence and skips this one.
func (v *treeValidator) checkDuplicateID(filePath, relPath string) bool {
	mdFile, err := markdown.LoadMarkdownFile(filePath)
	if err != nil {
		// Already reported by checkFrontmatter; without content there is no id.
		return true
	}
	trimmedID := strings.TrimSpace(mdFile.GetFrontmatter().LeafWikiID)
	if trimmedID == "" {
		return true
	}
	if existing, ok := v.seenIDs[trimmedID]; ok {
		v.report(Problem{
			Kind:     ProblemDuplicateID,
			Severity: SeverityError,
			Path:     relPath,
			Detail:   fmt.Sprintf("leafwiki_id %q is also used by %s", trimmedID, existing),
			Hint:     "delete the leafwiki_id from one of the two files so the server assigns a fresh one",
		})
		return false
	}
	v.seenIDs[trimmedID] = relPath
	return true
}

// report records one finding.
func (v *treeValidator) report(problem Problem) {
	v.problems = append(v.problems, problem)
}

// relative turns an absolute path into the slash path a report shows.
func (v *treeValidator) relative(path string) string {
	rel, err := filepath.Rel(v.rootDir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
