package main

import (
	"context"

	"github.com/urfave/cli/v3"
)

// usageErrorWith reports a subcommand's flag parse failure and turns it into the
// given exit code: the root's handler does not run for subcommands, so a typo
// would otherwise exit 1 (FR-195).
func usageErrorWith(code int) func(context.Context, *cli.Command, error, bool) error {
	return func(_ context.Context, cmd *cli.Command, err error, _ bool) error {
		_ = reportUsageError(context.Background(), cmd, err, false)
		return &exitCodeError{code: code, err: nil}
	}
}
