package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/perber/wiki/internal/wiki/control"
)

// errIsChannelRefused, errIsChannelUnavailable and errIsUsage are the three
// classifications the exit-code table needs.
func errIsChannelRefused(err error) bool     { return errors.Is(err, errChannelRefused) }
func errIsChannelUnavailable(err error) bool { return errors.Is(err, errChannelUnavailable) }
func errIsUsage(err error) bool              { return errors.Is(err, errUsage) }

// errUsage marks a wrong command line that the flag parser did not catch.
var errUsage = errors.New("invalid usage")

// defaultWaitTimeout is how long a write waits for its vectors (FR-163).
const defaultWaitTimeout = 30 * time.Second

// pageOptions are the flags every page command shares.
type pageOptions struct {
	user         string
	password     string
	passwordFile string
	// controlSocket overrides the discovered socket (FR-193b).
	controlSocket string
}

func (o *pageOptions) flags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "print the result as one JSON object"},
		&cli.StringFlag{Name: "user", Usage: "account to authenticate as", Sources: envVars("LEAFWIKI_AGENT_USER"), Config: trimmed, Destination: &o.user},
		&cli.StringFlag{Name: "password", Usage: "password for --user", Sources: envVars("LEAFWIKI_AGENT_PASSWORD"), Config: trimmed, Destination: &o.password},
		&cli.StringFlag{Name: "password-file", Usage: "file holding the password for --user", Destination: &o.passwordFile},
		&cli.StringFlag{Name: "control-socket", Usage: "override the socket path", Destination: &o.controlSocket},
	}
}

// credentials resolves the credential flags (FR-193): a user without a password
// is a usage error, and no user means anonymous.
func (o *pageOptions) credentials() (credentials, error) {
	// The documented order (FR-193): the flag, then the file, then the
	// environment. A Sources fallback on --password would put the environment
	// ahead of the file.
	user := o.user
	if user == "" {
		user = strings.TrimSpace(os.Getenv("LEAFWIKI_AGENT_USER"))
	}
	password := o.password
	if password == "" && o.passwordFile != "" {
		payload, err := os.ReadFile(o.passwordFile)
		if err != nil {
			return credentials{}, fmt.Errorf("%w: reading --password-file: %s", errUsage, err)
		}
		password = strings.TrimSpace(string(payload))
	}
	if password == "" {
		password = strings.TrimSpace(os.Getenv("LEAFWIKI_AGENT_PASSWORD"))
	}
	if user == "" {
		if password != "" {
			return credentials{}, fmt.Errorf("%w: a password without a --user", errUsage)
		}
		return credentials{}, nil
	}
	if password == "" {
		return credentials{}, fmt.Errorf("%w: --user needs --password, --password-file or LEAFWIKI_AGENT_PASSWORD", errUsage)
	}
	return credentials{user: user, password: password}, nil
}

// newPageCommand builds the page command family.
func newPageCommand(cfg *serverConfig) *cli.Command {
	options := &pageOptions{}
	page := &cli.Command{
		Name:  "page",
		Usage: "Read and write pages through the running server",
		Before: func(_ context.Context, _ *cli.Command) (context.Context, error) {
			// Results own stdout; log lines must not end up in them (FR-197).
			setupLogger(os.Stderr, "text")
			return context.Background(), nil
		},
	}

	// A typo of the group itself must be a usage error, not a help dump on
	// stdout or the "not found" exit code (FR-195).
	page.OnUsageError = usageErrorWith(exitCodeUsage)
	page.Action = func(_ context.Context, cmd *cli.Command) error {
		if name := cmd.Args().First(); name != "" {
			_, _ = fmt.Fprintf(cmd.Root().ErrWriter, "unknown page command %q\n", name)
			return &exitCodeError{code: exitCodeUsage, err: nil}
		}
		_, _ = fmt.Fprintln(cmd.Root().ErrWriter, "page needs one of: get, write, apply, delete")
		return &exitCodeError{code: exitCodeUsage, err: nil}
	}
	page.Commands = []*cli.Command{
		newPageGetCommand(cfg, options),
		newPageWriteCommand(cfg, options),
		newPageApplyCommand(cfg, options),
		newPageDeleteCommand(cfg, options),
	}
	// The shared flags are declared once on each subcommand, so --json and the
	// credential flags read the same everywhere.
	for _, command := range page.Commands {
		command.Flags = append(command.Flags, options.flags()...)
		command.OnUsageError = usageErrorWith(exitCodeUsage)
	}
	return page
}

// newStatusCommand builds the status command.
func newStatusCommand(cfg *serverConfig) *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "Print what the control channel reports about this instance",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "json", Usage: "print the status as one JSON object"},
			&cli.StringFlag{Name: "control-socket", Usage: "override the socket path"},
		},
		Before: func(_ context.Context, _ *cli.Command) (context.Context, error) {
			setupLogger(os.Stderr, "text")
			return context.Background(), nil
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if cmd.String("control-socket") != "" {
				cfg.control.controlSocket = cmd.String("control-socket")
			}
			return runStatus(cfg, cmd.Bool("json"), os.Stdout)
		},
	}
}

// runStatus prints the channel's status (FR-121, FR-122).
func runStatus(cfg *serverConfig, asJSON bool, out io.Writer) error {
	client, code, err := openChannel(cfg, nil)
	if err != nil {
		return &exitCodeError{code: code, err: err}
	}
	defer client.close()

	raw, apiErr := client.call("status", nil)
	if apiErr != nil {
		return &exitCodeError{code: exitCodeForCall(client, apiErr), err: apiErr}
	}

	if asJSON {
		if _, err := fmt.Fprintln(out, string(raw)); err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
		return nil
	}

	var status control.Status
	if err := json.Unmarshal(raw, &status); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	lines := []string{
		"protocol: " + status.Protocol,
		"auth: " + status.Auth.Mode + disabledSuffix(status.Auth.Disabled),
		fmt.Sprintf("pages: %d", status.Pages),
		fmt.Sprintf("vectors: %s paused=%t pending=%d failed=%d encoder=%t",
			status.Vectors.Mode, status.Vectors.Paused, status.Vectors.Pending, status.Vectors.Failed, status.Vectors.EncoderAvailable),
		fmt.Sprintf("indexing: running=%t", status.Indexing.Running),
		"capabilities: " + strings.Join(status.Capabilities, ","),
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
	}
	return nil
}

func disabledSuffix(disabled bool) string {
	if disabled {
		return " (disabled)"
	}
	return ""
}

// openChannel resolves and dials the channel. Authentication is the caller's
// step: reads do not need it, writes do when credentials were given.
func openChannel(cfg *serverConfig, options *pageOptions) (*controlClient, int, error) {
	if options != nil && options.controlSocket != "" {
		cfg.control.controlSocket = options.controlSocket
	}
	where, err := resolveEndpoint(cfg)
	if err != nil {
		return nil, exitCodeFailure, err
	}
	client, err := dialControl(cfg, where)
	if err != nil {
		return nil, exitCodeForClientError(err), err
	}
	return client, exitCodeOK, nil
}

// newPageGetCommand builds "page get" (FR-131).
func newPageGetCommand(cfg *serverConfig, options *pageOptions) *cli.Command {
	return &cli.Command{
		Name:      "get",
		Usage:     "Read one page",
		ArgsUsage: "<path>",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "id", Usage: "read by page id instead of path"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return runPageGet(cfg, options, cmd.String("id"), cmd.Args().First(), cmd.Bool("json"), os.Stdout)
		},
	}
}

// runPageGet prints one page (FR-132).
func runPageGet(cfg *serverConfig, options *pageOptions, id, path string, asJSON bool, out io.Writer) error {
	if id == "" && path == "" {
		return &exitCodeError{code: exitCodeUsage, err: fmt.Errorf("%w: page get needs a path or --id", errUsage)}
	}
	creds, err := options.credentials()
	if err != nil {
		return &exitCodeError{code: exitCodeUsage, err: err}
	}
	client, code, err := openChannel(cfg, options)
	if err != nil {
		return &exitCodeError{code: code, err: err}
	}
	defer client.close()
	if creds.user != "" {
		if apiErr := client.login(creds); apiErr != nil {
			return &exitCodeError{code: exitCodeForCall(client, apiErr), err: apiErr}
		}
	}

	params := map[string]any{}
	if id != "" {
		params["id"] = id
	} else {
		params["path"] = path
	}
	raw, apiErr := client.call("page.get", params)
	if apiErr != nil {
		return &exitCodeError{code: exitCodeForCall(client, apiErr), err: apiErr}
	}
	if asJSON {
		if _, err := fmt.Fprintln(out, string(raw)); err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
		return nil
	}

	var view control.PageView
	if err := json.Unmarshal(raw, &view); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	lines := []string{
		"path: " + view.Path,
		"id: " + view.ID,
		"title: " + view.Title,
		"kind: " + view.Kind,
		"version: " + view.Version,
		"tags: " + strings.Join(view.Tags, ","),
		"aliases: " + strings.Join(view.Aliases, ","),
		"vectors: " + view.Vectors,
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return &exitCodeError{code: exitCodeFailure, err: err}
		}
	}
	if _, err := fmt.Fprintln(out, "content:"); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	if _, err := fmt.Fprintln(out, view.Content); err != nil {
		return &exitCodeError{code: exitCodeFailure, err: err}
	}
	return nil
}

// newPageDeleteCommand builds "page delete" (FR-146).
func newPageDeleteCommand(cfg *serverConfig, options *pageOptions) *cli.Command {
	return &cli.Command{
		Name:      "delete",
		Usage:     "Delete one page and, with --recursive, its subtree",
		ArgsUsage: "<path>",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "recursive", Usage: "delete the whole subtree; required when the page has children"},
			&cli.BoolFlag{Name: "dry-run", Usage: "report the impact and delete nothing"},
			&cli.BoolFlag{Name: "no-wait", Usage: "do not wait for the semantic index"},
			&cli.DurationFlag{Name: "wait", Usage: "how long to wait for the semantic index", Value: defaultWaitTimeout},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			path := cmd.Args().First()
			if path == "" {
				return &exitCodeError{code: exitCodeUsage, err: fmt.Errorf("%w: page delete needs a path", errUsage)}
			}
			params := control.ApplyParams{
				DryRun: cmd.Bool("dry-run"),
				Items:  []control.ApplyItem{{Path: path, Delete: true, Recursive: cmd.Bool("recursive")}},
			}
			return runPageApply(cfg, options, params, cmd.Bool("json"), cmd.Bool("no-wait"), cmd.Duration("wait"), os.Stdout, os.Stderr)
		},
	}
}
