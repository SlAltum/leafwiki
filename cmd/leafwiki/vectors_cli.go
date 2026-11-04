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
	"time"

	"github.com/urfave/cli/v3"

	"github.com/perber/wiki/internal/core/shared/advisorylock"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// exitCodeError carries a specific exit code through the single exit channel.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit code %d", e.code)
	}
	return e.err.Error()
}
func (e *exitCodeError) Unwrap() error { return e.err }

// newVectorsCommand builds the offline semantic-index command. It works on the
// data directory without starting the server: the CLI is for operators whose
// instance is stopped or cannot run inference.
func newVectorsCommand(cfg *serverConfig) *cli.Command {
	return &cli.Command{
		Name:  "vectors",
		Usage: "Build or inspect the semantic search index offline",
		Before: func(_ context.Context, _ *cli.Command) (context.Context, error) {
			// The summary owns stdout; log lines must not end up in it.
			setupLogger(os.Stderr, "text")
			return context.Background(), nil
		},
		Commands: []*cli.Command{
			{
				Name:      "build",
				Usage:     "Embed the pages into a new index build",
				ArgsUsage: "[path-or-id]",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "full", Usage: "rebuild every page instead of the ones given"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return runVectorsBuild(ctx, cfg, cmd.Bool("full"), cmd.Args().First())
				},
			},
			newVectorsSyncCommand(cfg),
			{
				Name:  "status",
				Usage: "Print the semantic index state",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "print the status as one JSON object"},
				},
				Action: func(_ context.Context, cmd *cli.Command) error {
					return runVectorsStatus(cfg, cmd.Bool("json"), os.Stdout)
				},
			},
		},
	}
}

// vectorsLockPath is the lock file the server and this command share.
func vectorsLockPath(dataDir string) string {
	return filepath.Join(dataDir, "vectors.db.lock")
}

// vectorPaths resolves the data-dir-relative locations the commands need.
func vectorPaths(cfg *serverConfig) (dataDir, modelDir string) {
	dataDir = cfg.server.dataDir
	modelDir = cfg.vectors.vectorsModelDir
	if modelDir == "" {
		modelDir = filepath.Join(dataDir, "models")
	}
	return dataDir, modelDir
}

// runVectorsStatus prints the state. It takes no lock: a read must not block the
// server, and the status only reads.
func runVectorsStatus(cfg *serverConfig, asJSON bool, out io.Writer) error {
	status, err := readVectorStatus(cfg)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}

	if asJSON {
		encoded, err := json.Marshal(status)
		if err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
		if _, err := fmt.Fprintln(out, string(encoded)); err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
		return nil
	}

	if _, err := fmt.Fprintf(out, "state: %s\npaused: %t\npending: %d\n", status.State, status.Paused, status.Pending); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	if status.BuildID != "" {
		if _, err := fmt.Fprintf(out, "build: %s (model %s, tokenizer %s)\n", status.BuildID, status.ModelID, status.TokenizerVersion); err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
	}
	if status.LastError != "" {
		if _, err := fmt.Fprintf(out, "last error: %s\n", status.LastError); err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
	}
	return nil
}

// readVectorStatus opens the vectors database read-only for a status report.
func readVectorStatus(cfg *serverConfig) (vectors.Status, error) {
	dataDir, _ := vectorPaths(cfg)
	path := filepath.Join(dataDir, "vectors.db")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return vectors.Status{State: vectors.StateOff}, nil
	}

	store, err := embed.OpenStore(path)
	if err != nil {
		return vectors.Status{}, err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			slog.Default().Warn("could not close the vectors database", "error", closeErr)
		}
	}()

	// The status compares the current build's identity against the model on
	// disk, so the manifest has to be read even for a read-only report.
	modelID := ""
	if _, modelDir := vectorPaths(cfg); modelDir != "" {
		if model, err := embed.Open(modelDir); err == nil {
			modelID = model.Manifest.ModelID
		}
	}

	service := vectors.NewService(store, nil, nil, modelID, vectors.TokenizerVersion, vectors.ModeManual, slog.Default())
	return service.Status(true)
}

// runVectorsBuild embeds pages into a new build. It holds the OS lock the
// server also takes, so an offline build and a running server cannot write the
// same database (FR-412).
func runVectorsBuild(ctx context.Context, cfg *serverConfig, full bool, target string) error {
	dataDir, modelDir := vectorPaths(cfg)

	// The lock order is fixed: the whole data directory first, then the vectors
	// database inside it (FR-166). A running server holds the directory, so an
	// offline build reports the same "in use" exit code as a concurrent build.
	wikiLock, err := acquireWikiLock(dataDir)
	if err != nil {
		if errors.Is(err, advisorylock.ErrLocked) {
			return &exitCodeError{code: exitCodeLocked, err: err}
		}
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	defer releaseWikiLock(wikiLock)

	lock, err := advisorylock.TryAcquire(vectorsLockPath(dataDir))
	if err != nil {
		if errors.Is(err, advisorylock.ErrLocked) {
			return &exitCodeError{code: exitCodeLocked, err: fmt.Errorf("another process is writing the semantic index: %w", err)}
		}
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			slog.Default().Warn("could not release the vectors lock", "error", releaseErr)
		}
	}()

	store, err := embed.OpenStore(filepath.Join(dataDir, "vectors.db"))
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			slog.Default().Warn("could not close the vectors database", "error", closeErr)
		}
	}()

	model, err := embed.Open(modelDir)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	encoder, err := openOfflineEncoder(cfg, modelDir)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	defer func() {
		if closeErr := encoder.Close(); closeErr != nil {
			slog.Default().Warn("could not close the encoder", "error", closeErr)
		}
	}()

	treeService, err := loadVectorTree(dataDir)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}

	service := vectors.NewService(store, encoder, treeService, model.Manifest.ModelID, vectors.TokenizerVersion, vectors.ModeManual, slog.Default())

	if full || target == "" {
		started := time.Now()
		written, err := service.Rebuild(nil)
		if err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
		elapsed := time.Since(started)
		if elapsed > 0 {
			fmt.Printf("embedded %d passages in %s (%.0f passages/s)\n", written, elapsed.Round(time.Millisecond), float64(written)/elapsed.Seconds())
		}
		return nil
	}

	pageID, err := resolvePageID(treeService, target)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	started := time.Now()
	if err := service.IndexPage(pageID); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	fmt.Printf("embedded page %s in %s\n", pageID, time.Since(started).Round(time.Millisecond))
	return nil
}

// loadVectorTree loads the page tree the offline commands read pages through.
func loadVectorTree(dataDir string) (*tree.TreeService, error) {
	treeService := tree.NewTreeService(dataDir)
	if err := treeService.LoadTree(); err != nil {
		return nil, err
	}
	return treeService, nil
}

// openOfflineEncoder loads the model and starts inference for an offline
// command, which is the only work in its process, so the runtime's default
// thread pool is fine.
func openOfflineEncoder(cfg *serverConfig, modelDir string) (*embed.Encoder, error) {
	model, err := embed.Open(modelDir)
	if err != nil {
		return nil, err
	}
	return embed.NewEncoder(model, embed.EncoderOptions{SharedLibraryPath: cfg.vectors.vectorsLibrary})
}

// resolvePageID accepts a page id or a route path.
func resolvePageID(treeService *tree.TreeService, target string) (string, error) {
	if page, err := treeService.FindPageByID(target); err == nil && page != nil {
		return page.ID, nil
	}
	page, err := treeService.FindPageByRoutePath(target)
	if err != nil || page == nil {
		return "", fmt.Errorf("no page matches %q", target)
	}
	return page.ID, nil
}
