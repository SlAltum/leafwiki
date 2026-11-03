package launcher

import (
	"bytes"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
)

// Branding is the wiki's own identity as the window shows it (FR-110): the
// configured site name and logo, with LeafWiki as the fallback for both.
type Branding struct {
	Name string
	Logo image.Image
}

// brandingFileName is the wiki's settings file the launcher reads, next to the
// data directory.
const brandingFileName = "branding.json"

// LoadBranding reads <data>/branding.json and the logo it points at. Every
// failure falls back to the shipped identity instead of blocking the start:
// the window has to open even with a broken settings file.
func LoadBranding(dataDir string, fallbackName string) Branding {
	branding := Branding{Name: fallbackName}

	raw, err := os.ReadFile(filepath.Join(dataDir, brandingFileName))
	if err != nil {
		return branding
	}

	var config struct {
		SiteName    string `json:"siteName"`
		LogoFile    string `json:"logoFile"`
		FaviconFile string `json:"faviconFile"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return branding
	}

	if name := strings.TrimSpace(config.SiteName); name != "" {
		branding.Name = name
	}
	branding.Logo = loadLogo(dataDir, config.LogoFile, config.FaviconFile)
	return branding
}

// loadLogo decodes the configured logo, falling back to the favicon when only
// that one is a raster image (an SVG favicon is the web's, not the window's).
func loadLogo(dataDir, logoFile, faviconFile string) image.Image {
	for _, name := range []string{logoFile, faviconFile} {
		if name == "" || strings.ContainsAny(name, "/\\") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dataDir, "branding", name))
		if err != nil {
			continue
		}
		if img, _, err := image.Decode(bytes.NewReader(raw)); err == nil {
			return img
		}
	}
	return nil
}
