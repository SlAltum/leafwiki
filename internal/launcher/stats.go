package launcher

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// ContentStats is the ticket's third cell: what the data directory holds
// (FR-804's "数据目录" line in the prototype).
type ContentStats struct {
	Pages  int
	Assets int
	Bytes  int64
}

// Stats walks the content root once and counts pages, assets and bytes. It is
// cheap for a personal wiki and is called in the background, never per frame.
func Stats(dataDir string) ContentStats {
	var stats ContentStats

	root := filepath.Join(dataDir, "root")
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			stats.Bytes += info.Size()
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			stats.Pages++
		}
		return nil
	})

	assets := filepath.Join(dataDir, "assets")
	_ = filepath.WalkDir(assets, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		stats.Assets++
		if info, infoErr := entry.Info(); infoErr == nil {
			stats.Bytes += info.Size()
		}
		return nil
	})

	return stats
}
