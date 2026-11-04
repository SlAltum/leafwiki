package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/perber/wiki/internal/core/shared/advisorylock"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// syncReport is what one `vectors sync` run did: the counts the operator needs
// to judge whether the index caught up (FR-165).
type syncReport struct {
	State    string   `json:"state"`
	Scanned  int      `json:"scanned"`
	Embedded int      `json:"embedded"`
	Purged   int      `json:"purged"`
	Skipped  int      `json:"skipped"`
	Stale    int      `json:"stale"`
	Missing  int      `json:"missing"`
	Pending  int      `json:"pending,omitempty"`
	Failed   []string `json:"failed,omitempty"`
}

func newVectorsSyncCommand(cfg *serverConfig) *cli.Command {
	return &cli.Command{
		Name:  "sync",
		Usage: "Re-embed the pages whose vectors no longer match the Markdown",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "dry-run", Usage: "report what would be embedded, without a model"},
			&cli.BoolFlag{Name: "json", Usage: "print the summary as one JSON object"},
			&cli.StringFlag{Name: "paths", Usage: "comma-separated page paths or ids to restrict the run to"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return runVectorsSync(cfg, syncOptions{
				dryRun: cmd.Bool("dry-run"),
				asJSON: cmd.Bool("json"),
				paths:  splitPaths(cmd.String("paths")),
			}, os.Stdout)
		},
	}
}

// splitPaths turns the comma-separated --paths value into a list.
func splitPaths(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var paths []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	return paths
}

type syncOptions struct {
	dryRun bool
	asJSON bool
	paths  []string
	// encoder embeds without a model directory, which is how a test exercises
	// the write path; nil opens the real one.
	encoder vectors.Encoder
}

// runVectorsSync compares every page against the index, re-embeds the ones that
// drifted, and drops the pages the wiki no longer has (FR-165). It works
// offline, so it takes the data directory first and the index database second
// (FR-166).
func runVectorsSync(cfg *serverConfig, opts syncOptions, out io.Writer) error {
	dataDir, modelDir := vectorPaths(cfg)

	if _, err := os.Stat(dataDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &exitCodeError{code: exitCodeNotFound, err: fmt.Errorf("data directory %s does not exist", dataDir)}
		}
		return &exitCodeError{code: exitCodeFailure, err: err}
	}

	wikiLock, err := acquireWikiLock(dataDir)
	if err != nil {
		if errors.Is(err, advisorylock.ErrLocked) {
			return &exitCodeError{code: exitCodeLocked, err: err}
		}
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	defer releaseWikiLock(wikiLock)

	indexLock, err := advisorylock.TryAcquire(vectorsLockPath(dataDir))
	if err != nil {
		if errors.Is(err, advisorylock.ErrLocked) {
			return &exitCodeError{code: exitCodeLocked, err: fmt.Errorf("another process is writing the semantic index: %w", err)}
		}
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	defer func() {
		if releaseErr := indexLock.Release(); releaseErr != nil {
			slog.Default().Warn("could not release the vectors lock", "error", releaseErr)
		}
	}()

	dbPath := filepath.Join(dataDir, "vectors.db")
	if _, statErr := os.Stat(dbPath); errors.Is(statErr, os.ErrNotExist) {
		// Nothing was ever embedded, so there is nothing to compare against.
		if err := writeSyncReport(out, syncReport{State: string(vectors.StateOff)}, opts.asJSON); err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
		return nil
	}

	store, err := embed.OpenStore(dbPath)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			slog.Default().Warn("could not close the vectors database", "error", closeErr)
		}
	}()

	treeService, err := loadVectorTree(dataDir)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	allowed, err := resolvePathFilter(treeService, opts.paths)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}

	modelID := ""
	if model, modelErr := embed.Open(modelDir); modelErr == nil {
		modelID = model.Manifest.ModelID
	}
	// The comparison needs no encoder, which is what makes --dry-run work
	// without a model directory (FR-165b).
	service := vectors.NewService(store, nil, treeService, modelID, vectors.TokenizerVersion, vectors.ModeManual, slog.Default())

	diffs, err := service.DiffPages()
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}

	report := syncReport{Scanned: len(diffs)}
	var targets []string
	for _, diff := range diffs {
		// The filter comes first, so every counter describes this run's scope and
		// one page cannot land in two buckets.
		if allowed != nil && !allowed[diff.PageID] {
			report.Skipped++
			continue
		}
		switch diff.State {
		case vectors.PageIndexed:
			report.Skipped++
		case vectors.PageStale:
			report.Stale++
			targets = append(targets, diff.PageID)
		case vectors.PageMissing, vectors.PageStateOff:
			report.Missing++
			targets = append(targets, diff.PageID)
		default:
			// queued or failed: the page is in the index but not up to date.
			report.Pending++
			targets = append(targets, diff.PageID)
		}
	}
	report.State = syncState(service)

	if opts.dryRun {
		return finishSync(out, report, opts.asJSON)
	}

	closeEncoder := func() {}
	if opts.encoder == nil {
		opened, err := openOfflineEncoder(cfg, modelDir)
		if err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
		closeEncoder = func() {
			if closeErr := opened.Close(); closeErr != nil {
				slog.Default().Warn("could not close the encoder", "error", closeErr)
			}
		}
		opts.encoder = opened
	}
	defer closeEncoder()

	service = vectors.NewService(store, opts.encoder, treeService, modelID, vectors.TokenizerVersion, vectors.ModeManual, slog.Default())
	result, err := service.SyncPages(targets)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	report.Embedded = result.Pages
	report.Failed = result.Failed

	// An orphan is only unambiguous over the whole tree, so a --paths run leaves
	// the other pages alone.
	if allowed == nil {
		purged, err := purgeOrphans(service, store)
		if err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
		report.Purged = purged
	}

	// Read the state after the run: before it, the answer describes the index the
	// run is about to repair.
	report.State = syncState(service)
	return finishSync(out, report, opts.asJSON)
}

// syncState reports the index state, or fresh when the state cannot be read.
func syncState(service *vectors.Service) string {
	status, err := service.Status(true)
	if err != nil {
		return string(vectors.StateFresh)
	}
	return string(status.State)
}

// resolvePathFilter turns --paths into the page ids the run may embed. A nil
// result means every page; an unknown path fails the run instead of silently
// doing nothing.
func resolvePathFilter(treeService *tree.TreeService, paths []string) (map[string]bool, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	allowed := make(map[string]bool, len(paths))
	for _, path := range paths {
		pageID, err := resolvePageID(treeService, path)
		if err != nil {
			return nil, err
		}
		allowed[pageID] = true
	}
	return allowed, nil
}

// purgeOrphans drops the chunks of pages the wiki no longer has (FR-165).
func purgeOrphans(service *vectors.Service, store *embed.Store) (int, error) {
	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		return 0, err
	}
	orphans, err := service.Orphans(current.ID)
	if err != nil {
		return 0, err
	}
	for _, pageID := range orphans {
		if err := service.Forget(pageID); err != nil {
			return 0, err
		}
	}
	return len(orphans), nil
}

// finishSync prints the report and maps a partial run onto its exit code.
func finishSync(out io.Writer, report syncReport, asJSON bool) error {
	if err := writeSyncReport(out, report, asJSON); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	if len(report.Failed) > 0 {
		return &exitCodeError{
			code: exitCodeFailure,
			err:  fmt.Errorf("%d page(s) could not be embedded", len(report.Failed)),
		}
	}
	return nil
}

// writeSyncReport prints the summary as one JSON object or as text lines.
func writeSyncReport(out io.Writer, report syncReport, asJSON bool) error {
	if asJSON {
		encoded, err := json.Marshal(report)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}
	lines := []string{
		fmt.Sprintf("state: %s", report.State),
		fmt.Sprintf("scanned: %d", report.Scanned),
		fmt.Sprintf("embedded: %d", report.Embedded),
		fmt.Sprintf("purged: %d", report.Purged),
		fmt.Sprintf("skipped: %d", report.Skipped),
		fmt.Sprintf("stale: %d", report.Stale),
		fmt.Sprintf("missing: %d", report.Missing),
	}
	for _, pageID := range report.Failed {
		lines = append(lines, "failed: "+pageID)
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}
