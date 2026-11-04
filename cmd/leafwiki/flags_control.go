package main

import (
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/perber/wiki/internal/wiki/control"
)

// controlOptions holds the local agent channel's options
// (docs/srs/agent-operations.srs.md §4.11).
type controlOptions struct {
	controlSocket string
	controlAuth   string
	// socketExplicit records whether --control-socket was given at all: an
	// explicit empty value disables the channel, the default starts it.
	socketExplicit bool
}

func (o *controlOptions) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "control-socket",
			Destination: &o.controlSocket,
			Category:    catControl,
			Usage:       "unix socket an agent writes through; defaults to <data-dir>/control.sock, an empty value disables the channel",
			Sources:     envVars("LEAFWIKI_CONTROL_SOCKET"),
			Config:      trimmed,
		},
		&cli.StringFlag{
			Name:        "control-auth",
			Destination: &o.controlAuth,
			Category:    catControl,
			Usage:       "optional or required: whether an anonymous agent may read and write pages",
			Value:       string(control.AuthOptional),
			Sources:     envVars("LEAFWIKI_CONTROL_AUTH"),
			Config:      trimmed,
			Validator:   validateControlAuth,
		},
	}
}

// validateControlAuth rejects anything but the two documented modes.
func validateControlAuth(value string) error {
	if _, err := control.ParseAuthMode(value); err != nil {
		return err
	}
	return nil
}

// authMode is the parsed policy.
func (o *controlOptions) authMode() (control.AuthMode, error) {
	return control.ParseAuthMode(o.controlAuth)
}

// resolveSocketPath answers where the channel listens, and whether it is on at
// all: the flag may point elsewhere, the default follows the data directory, and
// an explicit empty value switches the channel off.
func (o *controlOptions) resolveSocketPath(dataDir string) (string, bool) {
	if o.socketExplicit && o.controlSocket == "" {
		return "", false
	}
	if o.controlSocket != "" {
		return o.controlSocket, true
	}
	if dataDir == "" {
		return "", false
	}
	return filepath.Join(dataDir, "control.sock"), true
}

// controlSocketFlagName is the flag the setup records as explicitly set.
const controlSocketFlagName = "control-socket"
