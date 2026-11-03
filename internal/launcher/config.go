// Package launcher holds every policy of the embedded desktop launcher: the
// files it owns next to the data directory, the server it starts, and the state
// the window shows. It deliberately imports no UI package, so all of it is
// testable without a display (NFR-8).
package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/perber/wiki/internal/core/shared"
)

// configFileName is the launcher's own settings file, next to the data
// directory (SRS §6.3).
const configFileName = "launcher.json"

// configVersion is the only version written. A file claiming a higher one is
// still read field by field, so an older launcher keeps working.
const configVersion = 1

// Config is launcher.json: the parameter overrides saved on the advanced page
// and the auto-refresh switch.
type Config struct {
	Version     int               `json:"version"`
	Params      map[string]string `json:"params"`
	AutoRefresh bool              `json:"auto_refresh"`
}

// DefaultConfig is what a first start uses.
func DefaultConfig() Config {
	return Config{Version: configVersion, Params: map[string]string{}}
}

// Load reads the launcher configuration. A missing file is not an error: that
// is simply a first start.
//
// keep decides which parameter names survive - the CLI's flag names minus the
// ones the launcher must not persist (FR-511), so a hand-edited file cannot
// smuggle an excluded flag, or a flag that no longer exists, into argv.
func Load(dataDir string, keep func(name string) bool) (Config, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, configFileName))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return DefaultConfig(), fmt.Errorf("read %s: %w", configFileName, err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return DefaultConfig(), fmt.Errorf("parse %s: %w", configFileName, err)
	}

	cfg.Version = configVersion
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	for name := range cfg.Params {
		if !keep(name) {
			delete(cfg.Params, name)
		}
	}
	return cfg, nil
}

// Save writes the configuration and only when its content actually changed, so
// opening the window and closing it again does not touch the file.
func (c Config) Save(dataDir string, keep func(name string) bool) error {
	out := Config{Version: configVersion, AutoRefresh: c.AutoRefresh, Params: map[string]string{}}
	for name, value := range c.Params {
		if keep(name) {
			out.Params[name] = value
		}
	}

	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", configFileName, err)
	}
	raw = append(raw, '\n')

	path := filepath.Join(dataDir, configFileName)
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, raw) {
		return nil
	}
	if err := shared.WriteFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", configFileName, err)
	}
	return nil
}
