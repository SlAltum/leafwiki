package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/perber/wiki/internal/core/tree"
)

// writeCheckTree writes a page below <dataDir>/root.
func writeCheckTree(t *testing.T, dataDir, relPath, content string) {
	t.Helper()

	path := filepath.Join(dataDir, "root", filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func checkExitCode(t *testing.T, err error) int {
	t.Helper()

	if err == nil {
		return 0
	}
	var coded *exitCodeError
	if !errors.As(err, &coded) {
		t.Fatalf("check = %v, want an exit code error", err)
	}
	return coded.code
}

func TestRunCheck_ExitCodes(t *testing.T) {
	newDataDir := func(t *testing.T, name string) string {
		t.Helper()
		dataDir := t.TempDir()
		switch name {
		case "clean":
			writeCheckTree(t, dataDir, "alpha.md", "# Alpha body")
		case "warning":
			writeCheckTree(t, dataDir, "bad_name.md", "# Bad body")
		case "error":
			writeCheckTree(t, dataDir, "docs.md", "# Docs file")
			writeCheckTree(t, dataDir, "docs/index.md", "# Docs section")
		}
		return dataDir
	}

	tests := []struct {
		name   string
		tree   string
		strict bool
		want   int
	}{
		{name: "clean tree", tree: "clean", want: 0},
		{name: "warning without --strict", tree: "warning", want: 0},
		{name: "warning with --strict", tree: "warning", strict: true, want: exitCodeFailure},
		{name: "error", tree: "error", want: exitCodeFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &serverConfig{server: serverOptions{dataDir: newDataDir(t, test.tree)}}
			var out bytes.Buffer
			err := runCheck(cfg, true, test.strict, &out)
			if got := checkExitCode(t, err); got != test.want {
				t.Fatalf("exit code = %d, want %d (output %q)", got, test.want, out.String())
			}
			var report tree.ValidationReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("invalid JSON %q: %v", out.String(), err)
			}
		})
	}
}

func TestRunCheck_MissingDataDirIsNotFound(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "does-not-exist")

	err := runCheck(&serverConfig{server: serverOptions{dataDir: dataDir}}, false, false, &bytes.Buffer{})
	if got := checkExitCode(t, err); got != exitCodeNotFound {
		t.Fatalf("exit code = %d, want %d", got, exitCodeNotFound)
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the check created %s", dataDir)
	}
}

// FR-181: the check leaves the tree, and the data directory, exactly as it was.
func TestRunCheck_WritesNothing(t *testing.T) {
	dataDir := t.TempDir()
	writeCheckTree(t, dataDir, "alpha.md", "# Alpha body")
	writeCheckTree(t, dataDir, "bad_name.md", "# Bad body")

	before := hashCheckTree(t, dataDir)
	if err := runCheck(&serverConfig{server: serverOptions{dataDir: dataDir}}, false, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("check = %v", err)
	}
	if after := hashCheckTree(t, dataDir); before != after {
		t.Fatalf("the check changed the data directory: before %s, after %s", before, after)
	}
}

// listTree lists every path below dataDir, relative and sorted.
func listTree(t *testing.T, dataDir string) []string {
	t.Helper()

	var paths []string
	err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dataDir, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(paths)
	return paths
}
func TestCheckCommand_UsageErrorsUseExitCodeTwo(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"leafwiki", "check", "--bogus"}},
		{name: "positional argument", args: []string{"leafwiki", "check", "extra"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := newRootCommandWithConfig(&serverConfig{})
			err := cmd.Run(context.Background(), test.args)

			var coded *exitCodeError
			if !errors.As(err, &coded) {
				t.Fatalf("check %v = %v, want an exit code error", test.args, err)
			}
			if coded.code != exitCodeUsage {
				t.Fatalf("exit code = %d, want %d", coded.code, exitCodeUsage)
			}
		})
	}
}

// hashCheckTree fingerprints every file below dataDir, names, mtimes and content.
func hashCheckTree(t *testing.T, dataDir string) string {
	t.Helper()

	sum := sha256.New()
	paths := listTree(t, dataDir)
	for _, rel := range paths {
		path := filepath.Join(dataDir, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.IsDir() {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		_, _ = io.WriteString(sum, rel+"|"+info.ModTime().UTC().String()+"|")
		_, _ = sum.Write(content)
	}
	return hex.EncodeToString(sum.Sum(nil))
}
