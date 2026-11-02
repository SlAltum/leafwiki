package wiki

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/perber/wiki/internal/avatar"
	"github.com/perber/wiki/internal/branding"
	"github.com/perber/wiki/internal/core/assets"
	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/core/email"
	"github.com/perber/wiki/internal/core/ignore"
	"github.com/perber/wiki/internal/core/shared/advisorylock"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/favorites"
	"github.com/perber/wiki/internal/links"
	"github.com/perber/wiki/internal/properties"
	"github.com/perber/wiki/internal/search"
	"github.com/perber/wiki/internal/sharing"
	"github.com/perber/wiki/internal/tags"
	"github.com/perber/wiki/internal/usersettings"
	wikipages "github.com/perber/wiki/internal/wiki/pages"
	"github.com/perber/wiki/internal/wiki/pagesave"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// ─── Subsystem initializers ───────────────────────────────────────────────────

func (w *Wiki) initAuth(options *WikiOptions) error {
	// A corrupt users.db fails here, before InitDefaultAdmin could create
	// an admin in an empty replacement (ADR-0013).
	store, err := auth.NewUserStore(w.storageDir)
	if err != nil {
		return fmt.Errorf("failed to init user store: %w", err)
	}
	w.user = auth.NewUserService(store)
	w.user.SetEditorLimit(options.EditorLimit)
	if !options.AuthDisabled {
		if err := w.user.InitDefaultAdmin(options.AdminUsername, options.AdminEmail, options.AdminPassword); err != nil {
			return err
		}
	}
	w.userResolver, err = auth.NewUserResolver(w.UserService)
	if err != nil {
		return err
	}
	if options.TOTPEncryptionKey != "" {
		totpService, err := auth.NewTOTPService([]byte(options.TOTPEncryptionKey))
		if err != nil {
			return fmt.Errorf("invalid TOTP encryption key: %w", err)
		}
		w.totp = totpService
	}
	if !options.AuthDisabled {
		sessionStore, err := auth.NewSessionStore(w.storageDir)
		if err != nil {
			return err
		}
		sessions := auth.NewSessionManager(sessionStore, options.JWTSecret, options.AccessTokenTimeout, options.RefreshTokenTimeout)
		w.auth = auth.NewAuthService(w.user, sessions, w.totp)

		// API keys are only meaningful when authentication is meaningful:
		// key management is admin-only and RequireAdmin already hard-blocks
		// admin operations when auth is disabled, so no key can be created
		// in that mode anyway. Keeping construction inside this block (like
		// sessionStore/w.auth) means w.APIKeyService() is nil when
		// AuthDisabled, which in turn keeps the Bearer middleware from being
		// registered at all — a key created before a later --disable-auth
		// restart must not keep authenticating (and narrowing/blocking)
		// requests in a mode where auth is supposed to be irrelevant.
		//
		// The feature is additionally gated behind EnableAPIKeyManagement:
		// it ships experimental and off by default, so w.apiKeys stays nil
		// (and the Bearer middleware/admin routes stay disabled) until an
		// operator explicitly opts in.
		if options.EnableAPIKeyManagement {
			apiKeyStore, err := auth.NewAPIKeyStore(w.storageDir)
			if err != nil {
				return err
			}
			w.apiKeys = auth.NewAPIKeyService(apiKeyStore, w.auth)
		}
	}
	return nil
}

// initEmail constructs the password-reset/invite token service when both
// SMTP is configured and auth is enabled — mirroring the EnableAPIKeyManagement
// block above, this keeps w.emailTokenService nil (and the corresponding
// routes returning ErrEmailDisabled) whenever either precondition is false,
// rather than constructing something unusable. Must run after initAuth,
// which sets w.user and w.auth.
func (w *Wiki) initEmail(options *WikiOptions) error {
	if !options.SMTP.Enabled() || options.AuthDisabled {
		return nil
	}

	store, err := auth.NewEmailTokenStore(w.storageDir)
	if err != nil {
		return err
	}
	w.emailTokenStore = store

	mailer := email.NewService(options.SMTP)
	w.emailTokenService = auth.NewEmailTokenService(store, w.user, w.auth, mailer, options.SMTP.PublicURL)
	return nil
}

func (w *Wiki) initCoreServices(options *WikiOptions) error {
	// Create a shared ignore cache for multi-level .leafwikiignore resolution.
	rootDir := filepath.Join(w.storageDir, "root")
	w.ignoreCache = ignore.NewCache(rootDir)

	w.tree = tree.NewTreeService(w.storageDir)
	w.tree.SetIgnoreCache(w.ignoreCache)
	if err := w.tree.LoadTree(); err != nil {
		return err
	}
	w.slug = tree.NewSlugService()
	w.asset = assets.NewAssetService(w.storageDir, w.slug)
	w.asset.SetIgnoreCache(w.ignoreCache)

	return nil
}

func (w *Wiki) initLinkService() error {
	linksStore, err := links.NewLinksStore(w.storageDir)
	if err != nil {
		return fmt.Errorf("failed to init links store: %w", err)
	}
	w.links = links.NewLinkService(w.storageDir, w.tree, linksStore)
	if err := w.links.IndexAllPages(); err != nil {
		w.log.Warn("failed to index links on startup", "error", err)
	}
	return nil
}

func (w *Wiki) initTagsService() error {
	tagsStore, err := tags.NewTagsStore(w.storageDir)
	if err != nil {
		return fmt.Errorf("failed to init tags store: %w", err)
	}
	w.tags = tags.NewTagsService(tagsStore)
	return nil
}

func (w *Wiki) initPropertiesService() error {
	propsStore, err := properties.NewPropertiesStore(w.storageDir)
	if err != nil {
		return fmt.Errorf("failed to init properties store: %w", err)
	}
	w.props = properties.NewPropertiesService(propsStore)
	return nil
}

func (w *Wiki) initFavoritesService() error {
	store, err := favorites.NewFavoritesStore(w.storageDir, w.log)
	if err != nil {
		return fmt.Errorf("failed to init favorites store: %w", err)
	}
	w.favorites = store
	return nil
}

func (w *Wiki) initUserSettingsService() error {
	store, err := usersettings.NewUserSettingsStore(w.storageDir, w.log)
	if err != nil {
		return fmt.Errorf("failed to init user settings store: %w", err)
	}
	w.userSettings = usersettings.NewUserSettingsService(store)
	return nil
}

// initShareLinks opens share_links.db only when the experimental share links
// feature (#1252) is enabled. Needs the tree from initCoreServices.
func (w *Wiki) initShareLinks(options *WikiOptions) error {
	if !options.EnableShareLinks {
		return nil
	}
	store, err := sharing.NewShareLinkStore(w.storageDir, w.log)
	if err != nil {
		return fmt.Errorf("failed to init share links store: %w", err)
	}
	w.shareLinks = sharing.NewShareLinkService(store, w.tree, w.log)
	return nil
}

// bootstrapTagsAndProperties clears and rebuilds tag and property indexes in a single
// parallel GetPages pass — avoids two sequential ReadPageRaw loops at startup.
func (w *Wiki) bootstrapTagsAndProperties() {
	if err := w.tags.ClearIndex(); err != nil {
		w.log.Warn("failed to clear tags index before bootstrap", "error", err)
		return
	}
	if err := w.props.ClearIndex(); err != nil {
		w.log.Warn("failed to clear properties index before bootstrap", "error", err)
		return
	}
	var ids []string
	if err := w.tree.WalkNodes(func(id string) error {
		ids = append(ids, id)
		return nil
	}); err != nil {
		w.log.Warn("failed to walk pages for tags/properties bootstrap", "error", err)
		return
	}
	pages, errs := w.tree.GetPages(ids)
	for i, page := range pages {
		if errs[i] != nil {
			w.log.Warn("skipping page during bootstrap", "pageID", ids[i], "error", errs[i])
			continue
		}
		if err := w.tags.IndexPageContent(page.ID, page.RawContent); err != nil {
			w.log.Warn("failed to index tags", "pageID", page.ID, "error", err)
		}
		if err := w.props.IndexPageContent(page.ID, page.RawContent); err != nil {
			w.log.Warn("failed to index properties", "pageID", page.ID, "error", err)
		}
	}
}

// vectorQueueInterval is how often a queued page is picked up. Long enough to
// merge a burst of saves, short enough that an editor sees the result.
const vectorQueueInterval = 5 * time.Second

func (w *Wiki) initSearch() error {
	var err error
	w.searchIndex, err = search.NewSQLiteIndex(w.storageDir)
	if err != nil {
		return fmt.Errorf("failed to init search index: %w", err)
	}
	w.status = search.NewIndexingStatus()
	searchEffect := pagesave.NewSearchIndexSideEffect(w.searchIndex, w.tree, w.log, w.metrics)
	w.log.Info("search indexing started")
	w.reloadWG.Add(1)
	go func() {
		defer w.reloadWG.Done()
		w.status.Start()
		defer w.status.Finish()
		if err := searchEffect.IndexAllPagesContext(w.shutdownCtx); err != nil {
			w.log.Warn("search bootstrap failed", "error", err)
			w.status.Fail()
		} else {
			w.log.Info("search indexing completed")
			w.status.Success()
		}
	}()
	return nil
}

// initVectors starts semantic search when it is switched on. A missing or
// broken model leaves the feature degraded instead of failing the boot: keyword
// search keeps working and the status says why vectors are unavailable
// (FR-402, FR-414). Only an untagged build is a hard error, because --vectors
// only means something with the inference runtime compiled in (C-7).
func (w *Wiki) initVectors(options *WikiOptions) error {
	mode, err := vectors.ParseMode(options.Vectors.Mode)
	if err != nil {
		return err
	}
	if !mode.Enabled() {
		return nil
	}
	// An untagged build has no inference runtime, and this option only means
	// something with one: refuse the start instead of running degraded by
	// accident (C-7).
	if !embed.EncoderAvailable() {
		return fmt.Errorf("semantic search needs a build with the vectors tag, but --vectors=%s was requested", mode)
	}

	// The same OS lock the offline CLI takes: a running server and an offline
	// build must never write the same database (FR-412).
	lock, err := advisorylock.TryAcquire(filepath.Join(w.storageDir, "vectors.db.lock"))
	if err != nil {
		w.log.Warn("semantic search unavailable: another process holds the vectors lock", "error", err)
		return nil
	}
	w.vectorLock = lock

	store, err := embed.OpenStore(filepath.Join(w.storageDir, "vectors.db"))
	if err != nil {
		w.log.Warn("semantic search unavailable: could not open the vectors database", "error", err)
		return nil
	}

	encoder, encoderErr := w.openVectorEncoder(options, mode)
	if encoderErr != nil {
		if errors.Is(encoderErr, embed.ErrEncoderNotBuilt) {
			if closeErr := store.Close(); closeErr != nil {
				w.log.Warn("could not close the vectors database", "error", closeErr)
			}
			return fmt.Errorf("semantic search needs a build with the vectors tag: %w", encoderErr)
		}
		w.log.Warn("semantic search unavailable: the encoder did not start", "error", encoderErr)
	}

	// A typed nil pointer would satisfy the interface and panic on use, so the
	// degraded case hands over an untyped nil.
	var encoderPort vectors.Encoder
	if encoder != nil {
		encoderPort = encoder
	}

	w.vectorStore = store
	w.vectorEncoder = encoder
	w.vectorService = vectors.NewService(store, encoderPort, w.tree, w.vectorModelID(options), vectors.TokenizerVersion, mode, w.log)

	w.log.Info("semantic search enabled", "mode", string(mode), "model", w.vectorModelID(options))
	w.startQueueWorker()
	return nil
}

// startQueueWorker drains queued pages in the background. Only auto mode queues
// on save, so only auto mode needs a worker: a queue that nothing drains would
// leave the semantic index frozen at the last explicit build (FR-410).
func (w *Wiki) startQueueWorker() {
	if w.vectorService == nil || !w.vectorService.AutoIndex() || w.vectorService.EncoderAvailable() == false {
		return
	}

	w.reloadWG.Add(1)
	go func() {
		defer w.reloadWG.Done()
		ticker := time.NewTicker(vectorQueueInterval)
		defer ticker.Stop()
		for {
			select {
			case <-w.shutdownCtx.Done():
				return
			case <-ticker.C:
				if _, err := w.vectorService.Drain(0); err != nil && !errors.Is(err, vectors.ErrBuildRunning) {
					w.log.Warn("semantic queue drain failed", "error", err)
				}
			}
		}
	}()
}

// openVectorEncoder loads the model and starts the inference runtime.
func (w *Wiki) openVectorEncoder(options *WikiOptions, mode vectors.Mode) (*embed.Encoder, error) {
	modelDir := options.Vectors.ModelDir
	if modelDir == "" {
		modelDir = filepath.Join(w.storageDir, "models")
	}

	model, err := embed.Open(modelDir)
	if err != nil {
		return nil, err
	}
	return embed.NewEncoder(model, embed.EncoderOptions{
		SharedLibraryPath: options.Vectors.LibraryPath,
		IntraOpThreads:    1,
	})
}

// vectorModelID reports the model identity stored with a build, so a model
// change makes the current build stale (FR-408).
func (w *Wiki) vectorModelID(options *WikiOptions) string {
	modelDir := options.Vectors.ModelDir
	if modelDir == "" {
		modelDir = filepath.Join(w.storageDir, "models")
	}
	model, err := embed.Open(modelDir)
	if err != nil {
		return ""
	}
	return model.Manifest.ModelID
}

// VectorStatus reports the semantic index state; enabled is false while the
// feature is off.
func (w *Wiki) VectorStatus() (vectors.Status, error) {
	if w.vectorService == nil {
		return vectors.Status{State: vectors.StateOff}, nil
	}
	return w.vectorService.Status(true)
}

func (w *Wiki) initBranding() error {
	var err error
	w.branding, err = branding.NewBrandingService(w.storageDir)
	if err != nil {
		return fmt.Errorf("failed to init branding service: %w", err)
	}
	return nil
}

func (w *Wiki) initAvatarService() error {
	var err error
	w.avatar, err = avatar.NewAvatarService(w.storageDir)
	if err != nil {
		return fmt.Errorf("failed to init avatar service: %w", err)
	}
	return nil
}

func (w *Wiki) ensureBaselineRevisions() {
	var ids []string
	if err := w.tree.WalkNodes(func(id string) error {
		ids = append(ids, id)
		return nil
	}); err != nil {
		w.log.Warn("failed to enumerate pages for baseline revisions", "error", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	pages, pageErrs := w.tree.GetPages(ids)
	var valid []*tree.Page
	for i, p := range pages {
		if pageErrs[i] != nil {
			w.log.Warn("failed to load page for baseline revision", "pageID", ids[i], "error", pageErrs[i])
			continue
		}
		if p != nil {
			valid = append(valid, p)
		}
	}
	if len(valid) == 0 {
		return
	}
	errs := w.revision.RecordContentUpdates(valid, SYSTEM_USER_ID, "baseline")
	for i, err := range errs {
		if err != nil {
			w.log.Warn("baseline revision failed", "pageID", valid[i].ID, "error", err)
		}
	}
}

func (w *Wiki) EnsureWelcomePage() error {
	if w.tree.HasPages() {
		w.log.Info("Welcome page already exists, skipping creation")
		return nil
	}
	o := w.newPageOrchestrator()
	k := tree.NodeKindPage
	createOut, err := wikipages.NewCreatePageUseCase(w.tree, w.slug, o, w.log, w.metrics).Execute(
		context.Background(),
		wikipages.CreatePageInput{UserID: SYSTEM_USER_ID, Title: "Welcome to LeafWiki", Slug: "welcome-to-leafwiki", Kind: &k},
	)
	if err != nil {
		return err
	}
	p := createOut.Page

	// Set the content of the welcome page
	content := `# Welcome to LeafWiki!

LeafWiki – A fast wiki for people who think in folders, not feeds.
Single Go binary. Markdown on disk. No external database service.

LeafWiki is a lightweight, self-hosted wiki for runbooks, internal docs, and technical notes — built for fast writing and explicit structure. It keeps your content as plain Markdown on disk and gives you fast navigation, search, and editing — without running additional services.


---

## Features

- **Markdown-based** pages stored on disk (no database required)
- **Hierarchical navigation** with sections and pages
- **Full-text search** powered by SQLite FTS5
- **Asset management** (upload, rename, delete attachments)
- **Revision history** with snapshots and restore
- **Import** from Markdown zip archives
- **Branding** customization (site name, logo, favicon)
- **Multi-user** with role-based access control (admin / editor / viewer)
- **Public access mode** for read-only anonymous browsing

## Getting Started

1. Create your first page using the **+** button in the sidebar
2. Write in **Markdown** — headings, lists, code blocks, and links are all supported
3. Use **sections** to group related pages into a folder-like hierarchy
4. Upload files by dragging them into the editor

For more information, visit the [LeafWiki GitHub repository](https://github.com/perber/leafwiki).
`
	current, err := w.tree.GetPage(p.ID)
	if err != nil {
		return err
	}
	if _, err := wikipages.NewUpdatePageUseCase(w.tree, w.slug, o, w.log, w.metrics).Execute(
		context.Background(),
		wikipages.UpdatePageInput{UserID: SYSTEM_USER_ID, ID: p.ID, Version: current.Version(), Title: p.Title, Slug: p.Slug, Content: &content, Kind: &k},
	); err != nil {
		return err
	}

	return nil
}
