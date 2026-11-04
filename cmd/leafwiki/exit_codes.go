package main

import (
	"errors"

	"github.com/perber/wiki/internal/wiki/control"
)

// The process exit codes of every command (docs/srs/agent-operations.srs.md
// FR-195). They live in one table so a new failure cannot invent its own number.
const (
	// exitCodeOK is success.
	exitCodeOK = 0
	// exitCodeFailure is an unclassified failure.
	exitCodeFailure = 1
	// exitCodeUsage is a wrong command line.
	exitCodeUsage = 2
	// exitCodeNotFound is a page that does not exist.
	exitCodeNotFound = 3
	// exitCodeVersionConflict is an optimistic-lock conflict: read again.
	exitCodeVersionConflict = 4
	// exitCodeDenied is unauthenticated or not allowed.
	exitCodeDenied = 5
	// exitCodeUnavailable is unreachable, a timeout, or a restore running.
	exitCodeUnavailable = 6
	// exitCodeCapability is a capability the server does not have.
	exitCodeCapability = 7
	// exitCodePartial is a batch that wrote some items and failed one.
	exitCodePartial = 8
	// exitCodeLocked says another process owns the data directory.
	exitCodeLocked = 75
)

// exitCodeForRPC maps a JSON-RPC error onto the exit code (FR-196).
func exitCodeForRPC(apiErr *control.Error) int {
	if apiErr == nil {
		return exitCodeOK
	}
	kind := ""
	if apiErr.Data != nil {
		kind = apiErr.Data.Kind
	}

	switch apiErr.Code {
	case control.CodeUnauthenticated, control.CodeForbidden:
		return exitCodeDenied
	case control.CodeInvalidParams:
		return exitCodeUsage
	case control.CodeRateLimited, control.CodeRestoreInProgress:
		return exitCodeUnavailable
	case control.CodePageNotFound:
		switch kind {
		case control.KindPageVersionConflict:
			return exitCodeVersionConflict
		case control.KindPageSlugConflict, control.KindPageHasChildren:
			return exitCodeUsage
		default:
			return exitCodeNotFound
		}
	case control.CodeMethodNotFound:
		// A version mismatch: the caller may fall back (FR-123, §6.3).
		return exitCodeCapability
	default:
		return exitCodeFailure
	}
}

// exitCodeForCall maps a failed request: when the failure was in the transport,
// that is what decides (a timeout is unreachable, a cancel is a refusal), not the
// synthetic -32603 the client reports.
func exitCodeForCall(client *controlClient, apiErr *control.Error) int {
	if client != nil && client.lastErr != nil {
		return exitCodeForClientError(client.lastErr)
	}
	return exitCodeForRPC(apiErr)
}

// exitCodeForClientError maps the errors the client itself produces (FR-194).
func exitCodeForClientError(err error) int {
	switch {
	case errIsChannelRefused(err):
		return exitCodeDenied
	case errors.Is(err, errChannelTimeout):
		return exitCodeUnavailable
	case errIsChannelUnavailable(err):
		return exitCodeUnavailable
	case errIsUsage(err):
		return exitCodeUsage
	default:
		return exitCodeFailure
	}
}
