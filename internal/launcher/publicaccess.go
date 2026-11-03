package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/perber/wiki/internal/core/shared"
)

// publicAccessFileName is the file the server's public-access service reads
// before the wiki boots (SRS §6.6).
const publicAccessFileName = "public-access.json"

// publicReadBody is the exact shape SRS §6.6 specifies.
const publicReadBody = `{"enabled": true}` + "\n"

// EnsurePublicRead makes the wiki readable without a login on a fresh data
// directory, so the browser shows pages instead of a login form right after the
// first start (FR-302). An existing file is never touched: the user may have
// turned public access off on purpose (FR-303).
func EnsurePublicRead(dataDir string) error {
	path := filepath.Join(dataDir, publicAccessFileName)

	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", publicAccessFileName, err)
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("prepare %s: %w", dataDir, err)
	}
	if err := shared.WriteFileAtomic(path, []byte(publicReadBody), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", publicAccessFileName, err)
	}
	return nil
}
