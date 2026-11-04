package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/core/shared/advisorylock"
	"github.com/perber/wiki/internal/wiki"
	"github.com/perber/wiki/internal/wiki/control"
)

// newPageWriteCommand builds "page write": one item of a page.apply (FR-192).
func newPageWriteCommand(cfg *serverConfig, options *pageOptions) *cli.Command {
	return &cli.Command{
		Name:      "write",
		Usage:     "Create or update one page",
		ArgsUsage: "<path>",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "create", Usage: "create the page if it does not exist"},
			&cli.StringFlag{Name: "title", Usage: "page title"},
			&cli.StringFlag{Name: "content-file", Usage: "read the content from this file, - for stdin"},
			&cli.StringFlag{Name: "tags", Usage: "comma-separated tags"},
			&cli.StringFlag{Name: "aliases", Usage: "comma-separated aliases"},
			&cli.StringSliceFlag{Name: "property", Usage: "property as key=value; repeatable"},
			&cli.BoolFlag{Name: "no-wait", Usage: "do not wait for the semantic index"},
			&cli.DurationFlag{Name: "wait", Usage: "how long to wait for the semantic index", Value: defaultWaitTimeout},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			path := cmd.Args().First()
			if path == "" {
				return &exitCodeError{code: exitCodeUsage, err: fmt.Errorf("%w: page write needs a path", errUsage)}
			}
			item := control.ApplyItem{Path: path, Create: cmd.Bool("create")}
			if cmd.IsSet("title") {
				title := cmd.String("title")
				item.Title = &title
			}
			if cmd.IsSet("content-file") {
				content, err := readContent(cmd.String("content-file"))
				if err != nil {
					return &exitCodeError{code: exitCodeForClientError(err), err: err}
				}
				item.Content = &content
			}
			if cmd.IsSet("tags") {
				values := splitList(cmd.String("tags"))
				item.Tags = &values
			}
			if cmd.IsSet("aliases") {
				values := splitList(cmd.String("aliases"))
				item.Aliases = &values
			}
			if properties := parseProperties(cmd.StringSlice("property")); properties != nil {
				item.Properties = &properties
			}

			return runPageApply(cfg, options, control.ApplyParams{Items: []control.ApplyItem{item}},
				cmd.Bool("json"), cmd.Bool("no-wait"), cmd.Duration("wait"), os.Stdout, os.Stderr)
		},
	}
}

// newPageApplyCommand builds "page apply": a JSONL batch (FR-192).
func newPageApplyCommand(cfg *serverConfig, options *pageOptions) *cli.Command {
	return &cli.Command{
		Name:  "apply",
		Usage: "Apply a batch of page changes read as JSONL",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "batch", Usage: "read one JSON item per line from this file, - for stdin"},
			&cli.BoolFlag{Name: "dry-run", Usage: "report what would change and write nothing"},
			&cli.BoolFlag{Name: "no-wait", Usage: "do not wait for the semantic index"},
			&cli.DurationFlag{Name: "wait", Usage: "how long to wait for the semantic index", Value: defaultWaitTimeout},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if !cmd.IsSet("batch") {
				return &exitCodeError{code: exitCodeUsage, err: fmt.Errorf("%w: page apply needs --batch FILE (or --batch - for stdin)", errUsage)}
			}
			items, err := readBatch(cmd.String("batch"))
			if err != nil {
				return &exitCodeError{code: exitCodeForClientError(err), err: err}
			}
			return runPageApply(cfg, options, control.ApplyParams{Items: items, DryRun: cmd.Bool("dry-run")},
				cmd.Bool("json"), cmd.Bool("no-wait"), cmd.Duration("wait"), os.Stdout, os.Stderr)
		},
	}
}

// readContent reads a content file, or stdin for "-".
func readContent(path string) (string, error) {
	if path == "-" {
		payload, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("%w: reading stdin: %s", errUsage, err)
		}
		return string(payload), nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: reading %s: %s", errUsage, path, err)
	}
	return string(payload), nil
}

// readBatch reads JSONL items, or stdin for "-".
func readBatch(path string) ([]control.ApplyItem, error) {
	var reader io.Reader = os.Stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("%w: reading %s: %s", errUsage, path, err)
		}
		defer func() {
			_ = file.Close()
		}()
		reader = file
	}

	scanner := bufio.NewScanner(reader)
	// One item can carry a MiB of content, so the line limit has to allow it.
	scanner.Buffer(make([]byte, 0, 64<<10), control.MaxContentBytes+(1<<20))
	var items []control.ApplyItem
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var item control.ApplyItem
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item); err != nil {
			return nil, fmt.Errorf("%w: line %d is not an item: %s", errUsage, line, err)
		}
		if decoder.More() {
			return nil, fmt.Errorf("%w: line %d has more than one JSON value", errUsage, line)
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: reading the batch: %s", errUsage, err)
	}
	return items, nil
}

// batchSize reports the encoded size of a batch, which the server also caps.
func batchSize(params control.ApplyParams) int {
	encoded, err := json.Marshal(params)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// runPageWrite sends one item and prints its result (FR-192: page write is one
// page.apply item).
func runPageWrite(cfg *serverConfig, options *pageOptions, params control.ApplyParams, asJSON, noWait bool, waitFor time.Duration, out, errOut io.Writer) error {
	return runPageApply(cfg, options, params, asJSON, noWait, waitFor, out, errOut)
}

// runPageApply runs the batch, prints the results and maps the outcome onto the
// exit codes (FR-148, FR-196).
func runPageApply(cfg *serverConfig, options *pageOptions, params control.ApplyParams, asJSON, noWait bool, waitFor time.Duration, out, errOut io.Writer) error {
	if len(params.Items) > control.MaxApplyItems {
		return &exitCodeError{code: exitCodeUsage, err: fmt.Errorf("%w: a batch holds at most %d items", errUsage, control.MaxApplyItems)}
	}
	if size := batchSize(params); size > control.MaxApplyBytes {
		return &exitCodeError{code: exitCodeUsage, err: fmt.Errorf("%w: the batch is %d bytes, the limit is %d", errUsage, size, control.MaxApplyBytes)}
	}
	creds, err := options.credentials()
	if err != nil {
		return &exitCodeError{code: exitCodeUsage, err: err}
	}
	client, code, err := openChannel(cfg, options)
	if err != nil {
		// Only an unreachable channel may fall back. A refusal (EACCES, a peer
		// that is not us, a hangup before the handshake) never does (FR-194).
		if code != exitCodeUnavailable {
			return &exitCodeError{code: code, err: err}
		}
		handled, offlineCode, offlineErr := runOffline(cfg, options, params, asJSON, out, errOut)
		if !handled {
			return &exitCodeError{code: code, err: err}
		}
		return &exitCodeError{code: offlineCode, err: offlineErr}
	}
	defer client.close()
	if creds.user != "" {
		if apiErr := client.login(creds); apiErr != nil {
			return &exitCodeError{code: exitCodeForRPC(apiErr), err: apiErr}
		}
	}

	raw, apiErr := client.call("page.apply", params)
	if apiErr != nil {
		return &exitCodeError{code: exitCodeForCall(client, apiErr), err: apiErr}
	}

	var result control.ApplyResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}

	waitCode := exitCodeOK
	if !params.DryRun && !noWait && waitFor > 0 {
		var waited control.ApplyResult
		waited, waitCode = waitForIndex(client, result, waitFor, errOut)
		result = waited
	}

	if err := printApplyResult(out, result, asJSON); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}

	failed := 0
	var firstFailure *control.ItemError
	for _, item := range result.Results {
		if item.Error != nil {
			failed++
			if firstFailure == nil {
				firstFailure = item.Error
			}
		}
	}
	if failed > 0 {
		// Some items were written: the batch is a partial success, and that
		// outranks a wait problem (FR-148: partial is always 8).
		_, _ = fmt.Fprintf(errOut, "wrote %d item(s), %d failed; the first failure is %s (%s)\n",
			len(result.Results)-failed, failed, firstFailure.Kind, firstFailure.Hint)
		return &exitCodeError{code: exitCodePartial, err: nil}
	}
	if waitCode != exitCodeOK {
		return &exitCodeError{code: waitCode, err: nil}
	}
	return nil
}

// runOffline is the fallback for an instance that is not running: it decides
// whether the offline path is allowed and runs the batch there (FR-171 to
// FR-175). handled is false when the caller should report its original error.
func runOffline(cfg *serverConfig, options *pageOptions, params control.ApplyParams, asJSON bool, out, errOut io.Writer) (bool, int, error) {
	dataDir := cfg.server.dataDir
	if dataDir == "" {
		return false, exitCodeUnavailable, nil
	}

	// Someone claims to be serving: the channel is unusable, not absent.
	for _, marker := range []string{"control.json", "launcher.state.json"} {
		if _, err := os.Stat(filepath.Join(dataDir, marker)); err == nil {
			_, _ = fmt.Fprintf(errOut, "the instance looks like it is running but its channel is unreachable (%s exists)\n", marker)
			return true, exitCodeLocked, nil
		}
	}

	// A dry run reports what the server would do; it needs the server, because
	// reading the tree here repairs files (FR-147: zero writes).
	if params.DryRun {
		return true, exitCodeUnavailable, fmt.Errorf("%w: --dry-run needs the running server; start it and try again", errUsage)
	}

	creds, err := options.credentials()
	if err != nil {
		return true, exitCodeUsage, err
	}

	if err := ensureDataDir(dataDir); err != nil {
		return true, exitCodeFailure, err
	}
	lock, err := acquireWikiLock(dataDir)
	if err != nil {
		if errors.Is(err, advisorylock.ErrLocked) {
			_, _ = fmt.Fprintln(errOut, "another process owns the data directory; wait for it to stop")
			return true, exitCodeLocked, nil
		}
		return true, exitCodeFailure, err
	}
	defer releaseWikiLock(lock)

	// The identity is the account when credentials were given, the system user
	// otherwise (FR-174).
	identity := control.SystemUserID
	role := ""
	if creds.user != "" {
		user, verifyErr := auth.VerifyCredentialsOffline(dataDir, creds.user, creds.password)
		if verifyErr != nil {
			return true, exitCodeForAuthError(verifyErr), verifyErr
		}
		identity = user.ID
		role = user.Role
	}

	offline, err := wiki.OpenOffline(dataDir, cfg.vectors.vectorsLibrary, slog.Default())
	if err != nil {
		return true, exitCodeFailure, err
	}
	defer func() {
		if closeErr := offline.Close(); closeErr != nil {
			_, _ = fmt.Fprintf(errOut, "warning: %v\n", closeErr)
		}
	}()

	_, _ = fmt.Fprintln(errOut, "the server is not running: writing offline, without revisions")

	result, apiErr := offline.Pages().PageApply(context.Background(), control.Caller{Identity: identity, Role: role}, params)
	if apiErr != nil {
		return true, exitCodeForRPC(apiErr), apiErr
	}

	if !params.DryRun {
		changed := make([]string, 0, len(result.Results))
		for index := range result.Results {
			item := &result.Results[index]
			if item.Changed && (item.Action == "create" || item.Action == "update") {
				changed = append(changed, item.Path)
			}
		}
		indexed, indexErr := offline.IndexChanged(changed)
		if indexErr != nil {
			_, _ = fmt.Fprintf(errOut, "warning: the semantic index was not updated (%v); run \"leafwiki vectors sync\" later\n", indexErr)
		} else if len(indexed) == 0 && len(changed) > 0 {
			_, _ = fmt.Fprintln(errOut, "warning: the semantic index may be stale; run \"leafwiki vectors sync\" while the server is stopped")
		}
		// Only what was really embedded says indexed; everything else keeps the
		// honest starting value (FR-175).
		for index := range result.Results {
			item := &result.Results[index]
			if indexed[item.Path] {
				item.Vector = "indexed"
			}
		}
	}

	if err := printApplyResult(out, result, asJSON); err != nil {
		return true, exitCodeFailure, err
	}
	failed := 0
	var firstFailure *control.ItemError
	for _, item := range result.Results {
		if item.Error != nil {
			failed++
			if firstFailure == nil {
				firstFailure = item.Error
			}
		}
	}
	if failed > 0 {
		_, _ = fmt.Fprintf(errOut, "wrote %d item(s), %d failed; the first failure is %s (%s)\n",
			len(result.Results)-failed, failed, firstFailure.Kind, firstFailure.Hint)
		return true, exitCodePartial, nil
	}
	return true, exitCodeOK, nil
}

// exitCodeForAuthError maps an offline credential failure (FR-174).
func exitCodeForAuthError(err error) int {
	switch {
	case errors.Is(err, auth.ErrUserAccountLocked), errors.Is(err, auth.ErrUserTOTPRequired),
		errors.Is(err, auth.ErrUserInvalidCredentials), errors.Is(err, auth.ErrUserNotFound):
		return exitCodeDenied
	default:
		return exitCodeFailure
	}
}

// waitForIndex polls page.get until the written pages are indexed (FR-163).
func waitForIndex(client *controlClient, result control.ApplyResult, timeout time.Duration, errOut io.Writer) (control.ApplyResult, int) {
	pending := []string{}
	for _, item := range result.Results {
		switch item.Vector {
		case "queued", "pending":
			pending = append(pending, item.Path)
		case "failed":
			return result, exitCodeCapability
		case "unavailable":
			// The server cannot index at all; waiting would only time out.
			_, _ = fmt.Fprintln(errOut, "warning: the server has no encoder, the index was not updated")
			return result, exitCodeCapability
		}
	}
	if len(pending) == 0 {
		return result, exitCodeOK
	}

	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			for index := range result.Results {
				if result.Results[index].Vector == "queued" || result.Results[index].Vector == "pending" {
					result.Results[index].Vector = "pending"
				}
			}
			_, _ = fmt.Fprintln(errOut, "warning: the semantic index did not finish in time; it keeps indexing in the background")
			return result, exitCodeUnavailable
		}

		// The answer is only "done" once every pending page was READ as indexed:
		// a failed poll means "not yet", never "fine" (FR-163).
		allDone := true
		for index := range result.Results {
			item := &result.Results[index]
			if item.Vector != "queued" && item.Vector != "pending" {
				continue
			}
			raw, apiErr := client.callWithin("page.get", map[string]any{"path": item.Path}, deadline)
			if apiErr != nil {
				if apiErr.Code == control.CodeMethodNotFound {
					return result, exitCodeCapability
				}
				// A rate limit or a transient failure is "keep waiting", not
				// "indexed".
				allDone = false
				break
			}
			var view control.PageView
			if err := json.Unmarshal(raw, &view); err != nil {
				allDone = false
				break
			}
			switch view.Vectors {
			case "indexed":
				item.Vector = "indexed"
			case "failed":
				item.Vector = "failed"
				return result, exitCodeCapability
			default:
				allDone = false
			}
		}
		if allDone {
			return result, exitCodeOK
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// printApplyResult writes the batch outcome: one JSON object or text lines.
func printApplyResult(out io.Writer, result control.ApplyResult, asJSON bool) error {
	if asJSON {
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}
	for _, item := range result.Results {
		line := item.Action + " " + item.Path
		if item.Changed {
			line += " (changed)"
		}
		if item.Error != nil {
			line += fmt.Sprintf(" (failed: %s: %s)", item.Error.Kind, item.Error.Hint)
		}
		if item.Vector != "" {
			line += " vector=" + item.Vector
		}
		if item.Irreversible {
			line += " irreversible"
		}
		if len(item.Affected) > 0 {
			line += " affects=" + strings.Join(item.Affected, ",")
		}
		if item.Assets > 0 {
			line += fmt.Sprintf(" assets=%d", item.Assets)
		}
		if item.Favorites > 0 {
			line += fmt.Sprintf(" favorites=%d", item.Favorites)
		}
		if item.HasRevisions {
			line += " revisions=true"
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

// splitList turns "a, b,,c" into [a b c].
func splitList(raw string) []string {
	var values []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

// parseProperties turns repeated key=value flags into a map.
func parseProperties(values []string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	properties := map[string]string{}
	for _, value := range values {
		key, val, found := strings.Cut(value, "=")
		if !found {
			properties[strings.TrimSpace(key)] = ""
			continue
		}
		properties[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	return properties
}

var _ = errors.Is
