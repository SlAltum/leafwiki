package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/perber/wiki/internal/core/tree"
)

func newCheckCommand(cfg *serverConfig) *cli.Command {
	return &cli.Command{
		Name:      "check",
		Usage:     "Check the page tree for content the server would skip or refuse",
		ArgsUsage: "(no arguments)",
		Description: "Reads the data directory and writes nothing. Exit codes: 0 nothing to fix, " +
			"1 findings to fix (or a data directory that cannot be read), 2 usage, 3 no such data directory.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "json", Usage: "print the findings as one JSON object"},
			&cli.BoolFlag{Name: "strict", Usage: "treat warnings as failures too"},
		},
		// A usage failure is its own exit code here (FR-184), not the root's
		// unclassified failure.
		OnUsageError: func(_ context.Context, cmd *cli.Command, err error, _ bool) error {
			if reportErr := reportUsageError(context.Background(), cmd, err, false); reportErr != nil {
				_ = reportErr
			}
			return &exitCodeError{code: exitCodeUsage, err: nil}
		},
		Before: func(_ context.Context, _ *cli.Command) (context.Context, error) {
			// The report owns stdout; log lines must not end up in it.
			setupLogger(os.Stderr, "text")
			return context.Background(), nil
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				_, _ = fmt.Fprintln(cmd.Root().ErrWriter, "check takes no arguments: "+cmd.Args().First())
				return &exitCodeError{code: exitCodeUsage, err: nil}
			}
			return runCheck(cfg, cmd.Bool("json"), cmd.Bool("strict"), os.Stdout)
		},
	}
}

// runCheck reports the findings and maps them onto the check exit codes
// (FR-184): 0 nothing to fix, 1 something to fix, 3 no data directory. It reads
// only: no writes, no stores, no locks (FR-181).
func runCheck(cfg *serverConfig, asJSON, strict bool, out io.Writer) error {
	dataDir := cfg.server.dataDir
	info, err := os.Stat(dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &exitCodeError{code: exitCodeNotFound, err: fmt.Errorf("data directory %s does not exist", dataDir)}
		}
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	if !info.IsDir() {
		return &exitCodeError{code: exitCodeFailure, err: fmt.Errorf("data directory %s is not a directory", dataDir)}
	}

	report, err := tree.ValidateTree(dataDir)
	if err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	if err := writeCheckReport(out, report, asJSON); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}

	if report.HasErrors() || (strict && report.HasWarnings()) {
		// Exit code 1 is the check's own "there is something to fix", not the
		// unclassified failure of FR-195 (FR-184).
		return &exitCodeError{code: exitCodeFailure, err: nil}
	}
	return nil
}

// writeCheckReport prints the report as one JSON object or as text lines.
func writeCheckReport(out io.Writer, report tree.ValidationReport, asJSON bool) error {
	if asJSON {
		encoded, err := json.Marshal(report)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}

	lines := []string{fmt.Sprintf("pages: %d", report.Pages), fmt.Sprintf("ignored: %d", report.Ignored)}
	for _, problem := range report.Problems {
		line := fmt.Sprintf("%s %s: %s", problem.Severity, problem.Kind, problem.Path)
		if problem.Detail != "" {
			line += " -- " + problem.Detail
		}
		if problem.Hint != "" {
			line += " (" + problem.Hint + ")"
		}
		lines = append(lines, line)
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}
