package wiki

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/perber/wiki/internal/core/assets"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/favorites"
	"github.com/perber/wiki/internal/wiki/control"
	"github.com/perber/wiki/internal/wiki/pagesave"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// Offline serves the page methods for a stopped instance: the same use cases,
// no side effects and no revision data (FR-172, FR-173). The caller holds the
// data directory lock, so nothing else writes meanwhile.
type Offline struct {
	pages   control.Pages
	tree    *tree.TreeService
	vectors *vectors.Service
	encoder *embed.Encoder
	store   *embed.Store
	favs    *favorites.FavoritesStore
	mu      sync.Mutex
}

// OpenOffline prepares the page methods against dataDir. Semantic indexing is
// only wired when the vectors database and the model directory both exist
// (FR-173b).
func OpenOffline(dataDir, libraryPath string, log *slog.Logger) (*Offline, error) {
	if log == nil {
		log = slog.Default()
	}
	treeService := tree.NewTreeService(dataDir)
	if err := treeService.LoadTree(); err != nil {
		return nil, fmt.Errorf("loading the page tree: %w", err)
	}

	slugger := tree.NewSlugService()
	assetsService := assets.NewAssetService(dataDir, slugger)
	favoritesStore, err := favorites.NewFavoritesStore(dataDir, log)
	if err != nil {
		return nil, fmt.Errorf("opening the favorites store: %w", err)
	}

	offline := &Offline{
		tree: treeService,
		favs: favoritesStore,
	}
	offline.pages = newControlPages(pageOps{
		tree:        treeService,
		slug:        slugger,
		newPipeline: func() *pagesave.PageSaveOrchestrator { return pagesave.NewPageSaveOrchestrator(nil) },
		assets:      assetsService,
		favorites:   favoritesStore,
		writeMu:     &offline.mu,
		log:         log,
	})

	modelDir := filepath.Join(dataDir, "models")
	if _, statErr := os.Stat(filepath.Join(dataDir, "vectors.db")); statErr == nil {
		if _, modelErr := os.Stat(modelDir); modelErr == nil {
			if err := offline.openVectors(dataDir, modelDir, libraryPath, treeService, log); err != nil {
				// A missing runtime or model is a degraded path, not a failure of
				// the command: the content still gets written (FR-173b).
				log.Warn("offline indexing unavailable, the semantic index stays stale", "error", err)
			}
		}
	}

	return offline, nil
}

// openVectors wires the semantic index for the offline run.
func (o *Offline) openVectors(dataDir, modelDir, libraryPath string, treeService *tree.TreeService, log *slog.Logger) error {
	store, err := embed.OpenStore(filepath.Join(dataDir, "vectors.db"))
	if err != nil {
		return err
	}
	model, err := embed.Open(modelDir)
	if err != nil {
		if closeErr := store.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}
	encoder, err := embed.NewEncoder(model, embed.EncoderOptions{SharedLibraryPath: libraryPath})
	if err != nil {
		if closeErr := store.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}

	o.store = store
	o.encoder = encoder
	o.vectors = vectors.NewService(store, encoder, treeService, model.Manifest.ModelID, vectors.TokenizerVersion, vectors.ModeManual, log)
	return nil
}

// Pages is what the command applies.
func (o *Offline) Pages() control.Pages { return o.pages }

// IndexChanged embeds the pages a batch wrote and reports which paths really
// ended up indexed (FR-173b, FR-175). A path the tree cannot resolve, or a page
// the embedder could not read, is not called indexed.
func (o *Offline) IndexChanged(paths []string) (map[string]bool, error) {
	indexed := map[string]bool{}
	if o.vectors == nil {
		return indexed, nil
	}

	idsByPath := map[string]string{}
	var ids []string
	for _, path := range paths {
		page, err := o.tree.FindPageByRoutePath(path)
		if err != nil || page == nil {
			continue
		}
		idsByPath[path] = page.ID
		ids = append(ids, page.ID)
	}
	if len(ids) == 0 {
		return indexed, nil
	}

	result, err := o.vectors.SyncPages(ids)
	if err != nil {
		return indexed, err
	}
	failed := map[string]bool{}
	for _, pageID := range result.Failed {
		failed[pageID] = true
	}
	for path, pageID := range idsByPath {
		if !failed[pageID] {
			indexed[path] = true
		}
	}
	return indexed, nil
}

// Close releases what the offline run opened.
func (o *Offline) Close() error {
	var errs []error
	if o.encoder != nil {
		if err := o.encoder.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if o.store != nil {
		if err := o.store.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if o.favs != nil {
		if err := o.favs.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
