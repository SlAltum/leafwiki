package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/perber/wiki/internal/backup"
	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/core/email"
	"github.com/perber/wiki/internal/core/ignore"
	"github.com/perber/wiki/internal/core/settings"
	"github.com/perber/wiki/internal/core/shared/advisorylock"
	sharedcrypto "github.com/perber/wiki/internal/core/shared/crypto"
	"github.com/perber/wiki/internal/core/tools"
	httpinternal "github.com/perber/wiki/internal/http"
	httpmetrics "github.com/perber/wiki/internal/http/metrics"
	authmw "github.com/perber/wiki/internal/http/middleware/auth"
	"github.com/perber/wiki/internal/publicaccess"
	"github.com/perber/wiki/internal/restore"
	"github.com/perber/wiki/internal/snapshot"
	"github.com/perber/wiki/internal/tocdisplay"
	"github.com/perber/wiki/internal/wiki"
	wikibackup "github.com/perber/wiki/internal/wiki/backup"
	"github.com/perber/wiki/internal/wiki/control"
	wikiinstancesettings "github.com/perber/wiki/internal/wiki/instancesettings"
	wikirestore "github.com/perber/wiki/internal/wiki/restore"
	wikisnapshot "github.com/perber/wiki/internal/wiki/snapshot"
	"github.com/urfave/cli/v3"
)

// Version is the LeafWiki build version. It defaults to "dev" for
// raw `go build`/`go run` (bypassing make); `make build`, `make run`, and the
// release/Docker builds all inject the real version resolved by
// scripts/resolve-version.sh via -ldflags "-X main.Version=v0.12.0".
var Version = "dev"

const (
	gitBackupSSHKeyFlagName       = "git-backup-ssh-key"
	gitBackupHTTPPasswordFlagName = "git-backup-http-password"
)

func setupLogger(w io.Writer, format string) {
	SetupLoggerWith(w, format, nil)
}

// SetupLoggerWith is setupLogger with an extra handler that also receives every
// record. The desktop launcher passes its ring buffer and log file here, so the
// window, stdout and the file see the same lines (FR-601).
func SetupLoggerWith(w io.Writer, format string, extra slog.Handler) {
	level := slog.LevelInfo
	switch os.Getenv("LEAFWIKI_LOG_LEVEL") {
	case "debug":
		level = slog.LevelDebug
	case "error":
		level = slog.LevelError
	case "warn":
		level = slog.LevelWarn
	}

	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
	}

	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	if extra != nil {
		handler = teeHandler{primary: handler, extra: extra}
	}

	slog.SetDefault(slog.New(handler))
}

// teeHandler hands every record to both handlers, which is how the launcher
// keeps its own sink next to the normal log stream (FR-601).
type teeHandler struct {
	primary slog.Handler
	extra   slog.Handler
}

func (h teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.primary.Enabled(ctx, level) || h.extra.Enabled(ctx, level)
}

func (h teeHandler) Handle(ctx context.Context, record slog.Record) error {
	// Each handler keeps its own level filter: the injected sink may want more
	// or less than the stream, and Enabled above only decides whether the
	// record reaches Handle at all.
	var errs []error
	if h.primary.Enabled(ctx, record.Level) {
		errs = append(errs, h.primary.Handle(ctx, record.Clone()))
	}
	if h.extra.Enabled(ctx, record.Level) {
		errs = append(errs, h.extra.Handle(ctx, record.Clone()))
	}
	return errors.Join(errs...)
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return teeHandler{primary: h.primary.WithAttrs(attrs), extra: h.extra.WithAttrs(attrs)}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	return teeHandler{primary: h.primary.WithGroup(name), extra: h.extra.WithGroup(name)}
}

// fail logs a startup failure and exits. main is its only caller: everything
// below it returns a fatalError instead, so the same message can reach a window
// (FR-111).
func fail(msg string, args ...any) {
	slog.Default().Error(msg, args...)
	os.Exit(1)
}

// fatalError is a startup failure: main logs it the way the CLI always has and
// exits 1. msg is the sentence for a person, cause the technical detail behind
// it, which the desktop launcher shows as the expandable second line (FR-701).
type fatalError struct {
	msg   string
	cause error
}

func (e *fatalError) Error() string {
	if e.cause == nil {
		return e.msg
	}
	return e.msg + ": " + e.cause.Error()
}

func (e *fatalError) Unwrap() error { return e.cause }

// failf builds a fatalError. cause may be nil when msg says everything.
func failf(msg string, cause error) error {
	return &fatalError{msg: msg, cause: cause}
}

var errRestoreSnapshotUsage = errors.New("restore-snapshot requires a path to a snapshot zip: leafwiki [--data-dir <DIR>] restore-snapshot <path-to-zip>")

// runRestoreSnapshotCommand implements the `restore-snapshot` command.
func runRestoreSnapshotCommand(dataDir, snapshotPath string) error {
	if snapshotPath == "" {
		return errRestoreSnapshotUsage
	}
	// An offline restore swaps the directories and databases of the whole data
	// directory, so it must not run next to a server (FR-101).
	if err := withWikiLock(dataDir, func() error {
		return restore.RestoreOffline(dataDir, snapshotPath)
	}); err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}
	fmt.Println("Snapshot restored successfully. Start the server normally to pick up the restored data.")
	return nil
}

var gracefulShutdownTimeout = 10 * time.Second

// errLogged reports a failure that has already been logged through the
// configured logger; errReported one that was written to stderr before the
// logger existed. main exits on either without reporting it a second time.
var (
	errLogged   = errors.New("logged error")
	errReported = errors.New("reported error")
)

func main() {
	// Errors that surface here come from flag parsing and validation, which
	// happen before there is a configured logger - they are CLI diagnostics, so
	// they go to stderr as plain text. Anything that fails once the logger is up
	// logs itself and returns errLogged.
	if err := newRootCommand().Run(context.Background(), os.Args); err != nil {
		// A command that owns its own exit code (the vectors lock) says so
		// explicitly; everything else goes through the one failure path.
		var coded *exitCodeError
		if errors.As(err, &coded) {
			if coded.err != nil {
				_, _ = fmt.Fprintln(os.Stderr, coded.err)
			}
			os.Exit(coded.code)
		}
		var fatal *fatalError
		if errors.As(err, &fatal) {
			if fatal.cause != nil {
				fail(fatal.msg, "error", fatal.cause)
			}
			fail(fatal.msg)
		}
		if !errors.Is(err, errLogged) && !errors.Is(err, errReported) {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(1)
	}
}

func runResetAdminPasswordCommand(cfg *serverConfig) error {
	// The reset rewrites the user database, which a running server also writes.
	var user *auth.User
	err := withWikiLock(cfg.server.dataDir, func() error {
		var lockErr error
		user, lockErr = tools.ResetAdminPassword(cfg.server.dataDir, cfg.auth.adminUsername, cfg.auth.adminEmail)
		return lockErr
	})
	if err != nil {
		var coded *exitCodeError
		if errors.As(err, &coded) {
			return err
		}
		if msg, ok := corruptDatabaseMessage(err); ok {
			return fmt.Errorf("password reset failed: %s", msg)
		}
		return fmt.Errorf("password reset failed: %w", err)
	}

	fmt.Println("Admin password reset successfully.")
	fmt.Printf("New password for user %s: %s\n", user.Username, user.Password)
	return nil
}

// Instance is a LeafWiki server running inside this process. It exists so a
// window can start and stop the server on demand instead of the process
// lifetime deciding it (FR-112): Addr reports where it listens, Wiki hands
// in-process callers the wiki itself (FR-402), Stop ends it.
type Instance struct {
	listener net.Listener
	wiki     *wiki.Wiki
	cancel   context.CancelFunc
	done     chan struct{}
	serveErr error
}

// Addr reports the address the server actually bound.
func (i *Instance) Addr() net.Addr { return i.listener.Addr() }

// Wiki is the wiki this instance serves.
func (i *Instance) Wiki() *wiki.Wiki { return i.wiki }

// Wait blocks until the instance has stopped and reports how serving ended.
func (i *Instance) Wait() error {
	<-i.done
	return i.serveErr
}

// Stop ends the instance and waits for it to release its resources. It fails
// when ctx expires before the server has stopped.
func (i *Instance) Stop(ctx context.Context) error {
	i.cancel()
	select {
	case <-i.done:
		return i.serveErr
	case <-ctx.Done():
		return fmt.Errorf("server did not stop in time: %w", ctx.Err())
	}
}

// validateServerConfig runs the checks that only inspect the resolved options.
// They return the failure instead of exiting so a window can show the same
// message the CLI logs (FR-111); main is the single place that logs and exits.
func validateServerConfig(cmd *cli.Command, cfg *serverConfig) error {
	if _, err := authmw.ParseTrustedProxies(cfg.proxy.trustedProxyIPs); err != nil {
		return failf("invalid --trusted-proxy-ips value", err)
	}
	if err := validateListenConfig(cfg.server.unixSocket, cmd.IsSet("host"), cmd.IsSet("port")); err != nil {
		return failf("Invalid listen configuration", err)
	}
	if err := validateHTTPRemoteUserConfig(cfg.proxy.enableHTTPRemoteUser, cfg.proxy.trustedProxyIPs); err != nil {
		return failf("Invalid HTTP remote user configuration", err)
	}
	if err := validateHTTPRemoteUserAutoCreateConfig(cfg.proxy.enableHTTPRemoteUserAutoCreate, cfg.proxy.enableHTTPRemoteUser, cfg.proxy.httpRemoteUserDefaultRole); err != nil {
		return failf("Invalid HTTP remote user auto-create configuration", err)
	}
	if err := validateSMTPConfig(cfg.email.smtpHost, cfg.email.smtpFrom, cfg.server.publicURL, cfg.email.smtpSecurity); err != nil {
		return failf("Invalid SMTP configuration", err)
	}
	// git-backup-remote is optional (local-only mode is supported)
	if cfg.backup.gitBackup {
		if err := validateGitBackupRemote(cfg.backup.gitBackupRemote, cfg.backup.gitBackupSSHKey, cfg.backup.gitBackupSSHKeyPath, cfg.backup.gitBackupHTTPUsername, cfg.backup.gitBackupHTTPPassword); err != nil {
			return failf("Invalid git backup configuration", err)
		}
	}
	return nil
}

// runServerCommand is the root action: with no subcommand given, leafwiki runs
// the wiki server and blocks until it stops.
func runServerCommand(ctx context.Context, cmd *cli.Command, cfg *serverConfig) error {
	if err := validateServerConfig(cmd, cfg); err != nil {
		return err
	}

	instance, err := runServerWithConfig(ctx, cfg)
	if err != nil {
		return err
	}

	if err := instance.Wait(); err != nil {
		slog.Default().Error("Failed to start server", "error", err)
		return errLogged
	}
	return nil
}

// runServerWithConfig binds the configured address and serves on it. The
// launcher binds its own listener and calls runServerWithListener instead
// (FR-112), which keeps the CLI path exactly as it was.
func runServerWithConfig(ctx context.Context, cfg *serverConfig) (*Instance, error) {
	// The data directory comes first: a second instance must fail without ever
	// touching this one's sockets or stores (FR-101).
	if err := ensureDataDir(cfg.server.dataDir); err != nil {
		return nil, failf("Failed to create data directory", err)
	}
	lock, err := acquireWikiLock(cfg.server.dataDir)
	if err != nil {
		return nil, failf("Failed to start server", err)
	}
	return listenAndServe(ctx, cfg, lock)
}

// listenAndServe binds the configured address and hands the bound instance the
// directory lock, which the instance releases when it has shut down.
func listenAndServe(ctx context.Context, cfg *serverConfig, lock *advisorylock.File) (*Instance, error) {
	if cfg.server.unixSocket == "" {
		listenAddr := cfg.server.host + ":" + cfg.server.port
		slog.Default().Info("Starting LeafWiki", "address", listenAddr, "data_dir", cfg.server.dataDir)
		listener, err := net.Listen("tcp", listenAddr)
		if err != nil {
			// Nothing took ownership of the lock, so this scope releases it.
			releaseWikiLock(lock)
			return nil, failf("Failed to start server", err)
		}
		return runServerWithListener(ctx, cfg, listener, lock)
	}

	listener, err := listenOnUnixSocket(cfg.server.unixSocket)
	if err != nil {
		releaseWikiLock(lock)
		return nil, failf("Failed to start server", err)
	}

	slog.Default().Info(
		"Starting LeafWiki",
		"unix_socket", cfg.server.unixSocket,
		"data_dir", cfg.server.dataDir,
		"host_port_overridden", true,
	)

	return runServerWithListener(ctx, cfg, listener, lock)
}

// runServerWithListener starts the server on listener and takes ownership of
// it and of lock: on failure both are released here, on success the lock is
// released once the wiki is closed, so a new instance can only take the
// directory after nothing of this one is left (FR-101). Everything the server
// needs is built here and released in reverse order once serving ends. Setup
// itself does not observe ctx: a cancelled ctx is checked once, before serving
// starts, and returns ctx.Err().
func runServerWithListener(ctx context.Context, cfg *serverConfig, listener net.Listener, lock *advisorylock.File) (*Instance, error) {
	var cleanup func()
	if cfg.server.unixSocket != "" {
		cleanup = func() {
			_ = listener.Close()
			_ = os.Remove(cfg.server.unixSocket)
		}
	}

	// cleanups are released in reverse creation order; closeWiki runs after
	// all of them, whoever ends the start attempt.
	var cleanups []func()
	var closeWiki func()
	abort := func(err error) (*Instance, error) {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
		if closeWiki != nil {
			closeWiki()
		}
		if cleanup != nil {
			cleanup()
		} else {
			_ = listener.Close()
		}
		releaseWikiLock(lock)
		return nil, err
	}

	basePath := NormalizeBasePath(cfg.server.basePath)
	maxAssetUploadSize, err := parseByteSize(cfg.frontend.maxAssetUploadSize)
	if err != nil {
		return abort(failf("Invalid max asset upload size", err))
	}
	restoreUploadMaxSize, err := parseByteSize(cfg.backup.restoreUploadMaxSize)
	if err != nil {
		return abort(failf("Invalid restore upload max size", err))
	}

	logoutURL := cfg.proxy.logoutURL
	if resolved, usedDeprecated := resolveLogoutURL(logoutURL, cfg.proxy.httpRemoteUserLogoutURL); usedDeprecated {
		slog.Default().Warn("--http-remote-user-logout-url/LEAFWIKI_HTTP_REMOTE_USER_LOGOUT_URL is deprecated, use --logout-url/LEAFWIKI_LOGOUT_URL instead")
		logoutURL = resolved
	}

	smtpEnabled := cfg.email.smtpHost != ""

	// validateServerConfig has already accepted this value; it is parsed a
	// second time because the router needs the result, not the verdict.
	trustedProxies, err := authmw.ParseTrustedProxies(cfg.proxy.trustedProxyIPs)
	if err != nil {
		return abort(failf("invalid --trusted-proxy-ips value", err))
	}

	if smtpEnabled {
		// A misconfigured public URL produces broken links in emails that
		// have already gone out — irreversible — so log the resolved value
		// once at boot rather than only on first use.
		slog.Default().Info("SMTP email enabled", "host", cfg.email.smtpHost, "port", cfg.email.smtpPort, "security", cfg.email.smtpSecurity, "publicUrl", cfg.server.publicURL)
	}

	if cfg.proxy.enableHTTPRemoteUser {
		slog.Default().Info("Reverse-proxy authentication enabled",
			"header", cfg.proxy.httpRemoteUserHeader,
			"trusted_proxies", cfg.proxy.trustedProxyIPs,
			"auto_create", cfg.proxy.enableHTTPRemoteUserAutoCreate,
			"default_role", cfg.proxy.httpRemoteUserDefaultRole,
		)
	}
	if cfg.metrics.enableMetrics {
		slog.Default().Info("Prometheus metrics enabled",
			"metrics_host", cfg.metrics.metricsHost,
			"metrics_port", cfg.metrics.metricsPort,
		)
	}

	if cfg.auth.disableAuth {
		slog.Default().Warn("Authentication disabled. Wiki is publicly accessible without authentication.")
	}

	if cfg.server.allowInsecure {
		slog.Default().Warn("allow-insecure enabled. Auth cookies may be transmitted over plain HTTP (INSECURE).")
	}

	// The directory may have disappeared between taking the lock and booting.
	if err := ensureDataDir(cfg.server.dataDir); err != nil {
		return abort(failf("Failed to create data directory", err))
	}

	publicAccessService, err := buildPublicAccessService(cfg)
	if err != nil {
		return abort(err)
	}

	tocDisplayService, err := tocdisplay.New(cfg.server.dataDir)
	if err != nil {
		return abort(failf("Failed to load toc-display configuration", err))
	}

	if !cfg.auth.disableAuth {
		if cfg.auth.jwtSecret == "" {
			return abort(failf("JWT secret is required. Set it using --jwt-secret or LEAFWIKI_JWT_SECRET environment variable.", nil))
		}

		if cfg.auth.adminPassword == "" {
			return abort(failf("admin password is required. Set it using --admin-password or LEAFWIKI_ADMIN_PASSWORD environment variable.", nil))
		}
	}

	var metrics *httpmetrics.HTTPMetrics
	if cfg.metrics.enableMetrics {
		metrics = httpmetrics.NewHTTPMetrics(Version)
		stopMetricsServer, _, err := startMetricsServer(metrics, cfg.metrics.metricsHost, cfg.metrics.metricsPort)
		if err != nil {
			return abort(failf("Failed to start metrics server", err))
		}
		cleanups = append(cleanups, stopMetricsServer)
	}

	// Initialize git backup before the wiki loads its tree: on first contact with
	// an existing backup this syncs its content into the data directory, so the
	// wiki boots with those pages and does not seed a welcome page on top of them.
	backupManager, err := buildBackupManager(cfg)
	if err != nil {
		return abort(failf("git backup init failed", err))
	}
	cleanups = append(cleanups, backupManager.Stop)

	// The restore write gate is built before the wiki: the HTTP middleware and
	// the agent channel must share one instance (FR-150).
	var writeGate *restore.WriteGate
	if cfg.backup.snapshot {
		writeGate = restore.NewWriteGate()
	}

	w, err := wiki.NewWiki(&wiki.WikiOptions{
		StorageDir:             cfg.server.dataDir,
		AdminUsername:          cfg.auth.adminUsername,
		AdminEmail:             cfg.auth.adminEmail,
		AdminPassword:          cfg.auth.adminPassword,
		JWTSecret:              cfg.auth.jwtSecret,
		TOTPEncryptionKey:      cfg.auth.totpEncryptionKey,
		AccessTokenTimeout:     cfg.auth.accessTokenTimeout,
		RefreshTokenTimeout:    cfg.auth.refreshTokenTimeout,
		AuthDisabled:           cfg.auth.disableAuth,
		EnableRevision:         cfg.frontend.enableRevision,
		EnableAPIKeyManagement: cfg.frontend.enableAPIKeyManagement,
		EnableShareLinks:       cfg.frontend.enableShareLinks,
		MaxRevisionHistory:     cfg.frontend.maxRevisionHistory,
		EditorLimit:            cfg.auth.editorLimit,
		RevisionCoalesceWindow: cfg.frontend.revisionCoalesceWindow,
		Vectors: wiki.VectorsOptions{
			Mode:        cfg.vectors.vectors,
			ModelDir:    cfg.vectors.vectorsModelDir,
			LibraryPath: cfg.vectors.vectorsLibrary,
		},
		SMTP: email.Config{
			Host:               cfg.email.smtpHost,
			Port:               cfg.email.smtpPort,
			Username:           cfg.email.smtpUsername,
			Password:           cfg.email.smtpPassword,
			From:               cfg.email.smtpFrom,
			FromName:           cfg.email.smtpFromName,
			Security:           email.Security(cfg.email.smtpSecurity),
			InsecureSkipVerify: cfg.email.smtpInsecureSkipVerify,
			Timeout:            cfg.email.smtpTimeout,
			PublicURL:          cfg.server.publicURL,
		},
		Metrics:   metrics,
		WriteGate: writeGate,
	})
	if err != nil {
		if msg, ok := corruptDatabaseMessage(err); ok {
			return abort(failf(msg, err))
		}
		return abort(failf("Failed to initialize Wiki", err))
	}

	w.SetInstanceSettingsRoutes(wikiinstancesettings.NewRoutes(publicAccessService, tocDisplayService, w.AuthService(), slog.Default()))

	// Log .leafwikiignore status
	rootDir := filepath.Join(cfg.server.dataDir, "root")
	if ignoreFile, err := ignore.LoadFromDir(rootDir); err != nil {
		slog.Default().Warn("invalid .leafwikiignore", "error", err)
	} else if ignoreFile != nil {
		slog.Default().Info("loaded .leafwikiignore", "patterns", ignoreFile.PatternCount())
	}

	// The wiki is closed after every other cleanup: an in-flight restore or
	// backup sync must not outlive the stores it writes through.
	closeWiki = func() {
		if err := w.Close(); err != nil {
			slog.Default().Error("Failed to close Wiki", "error", err)
		}
	}
	// A settings Reconfigure (or the settings-managed manager's own background
	// boot) can materialize remote content straight onto disk after the wiki's
	// tree/SQLite index has already loaded — keep the index from going stale by
	// triggering the same resync used for SIGUSR1/SIGHUP.
	backupManager.SetOnContentSynced(w.TriggerResyncAsync)
	w.SetBackupRoutes(wikibackup.NewRoutes(backupManager, w.AuthService()))

	// The local agent channel is a separate unix socket with JSON-RPC frames
	// (FR-102). A channel that cannot bind is a warning, never a refused start
	// (FR-104): keyword search and the UI must keep working.
	if socketPath, enabled := cfg.control.resolveSocketPath(cfg.server.dataDir); enabled {
		// The socket path must be absolute: a relative --data-dir would
		// otherwise make the channel refuse to start.
		if absolute, err := filepath.Abs(socketPath); err == nil {
			socketPath = absolute
		}
		authMode, modeErr := cfg.control.authMode()
		if modeErr != nil {
			return abort(failf("Invalid control channel configuration", modeErr))
		}
		channel, channelErr := control.New(control.Config{
			DataDir:      cfg.server.dataDir,
			SocketPath:   socketPath,
			AuthMode:     authMode,
			Source:       w,
			Pages:        w.ControlPages(),
			Capabilities: controlCapabilities,
			Logger:       slog.Default().With("component", "ControlChannel"),
		})
		if channelErr != nil {
			slog.Default().Warn("control channel unavailable", "error", channelErr)
			removeStaleControlDescriptor(cfg.server.dataDir)
		} else if startErr := channel.Start(); startErr != nil {
			slog.Default().Warn("control channel unavailable, the server keeps running without it", "error", startErr)
			removeStaleControlDescriptor(cfg.server.dataDir)
		} else {
			cleanups = append(cleanups, channel.Stop)
		}
	} else {
		// A descriptor from a crashed run would point an agent at a socket that
		// nobody serves (FR-106, FR-193b).
		removeStaleControlDescriptor(cfg.server.dataDir)
	}

	// Initialize full backup snapshots if enabled
	if cfg.backup.snapshot {
		snapshotsDir := cfg.backup.snapshotDir
		if snapshotsDir == "" {
			snapshotsDir = filepath.Join(cfg.server.dataDir, "snapshots")
		}
		snapshotManager := snapshot.NewManager(snapshot.Config{
			BackupsDir:         snapshotsDir,
			RootDir:            filepath.Join(cfg.server.dataDir, "root"),
			AssetsDir:          filepath.Join(cfg.server.dataDir, "assets"),
			BrandingDir:        filepath.Join(cfg.server.dataDir, "branding"),
			AvatarsDir:         filepath.Join(cfg.server.dataDir, "avatars"),
			BrandingConfigFile: filepath.Join(cfg.server.dataDir, "branding.json"),
			SettingsFiles: []string{
				filepath.Join(cfg.server.dataDir, "public-access.json"),
				filepath.Join(cfg.server.dataDir, "toc-display.json"),
			},
			SchemaFile:     filepath.Join(cfg.server.dataDir, "schema.json"),
			UsersDBPath:    filepath.Join(cfg.server.dataDir, "users.db"),
			StateDBPaths:   stateDBPaths(cfg.server.dataDir),
			WikiVersion:    Version,
			Interval:       cfg.backup.snapshotInterval,
			RetentionCount: cfg.backup.snapshotRetention,
		})
		snapshotScheduler := snapshot.NewScheduler(snapshotManager)
		cleanups = append(cleanups, snapshotScheduler.Stop)
		w.SetSnapshotRoutes(wikisnapshot.NewRoutes(snapshotManager, snapshotScheduler, w.AuthService(), cfg.backup.snapshotRetention))

		restoreManager := restore.NewManager(restore.Config{
			SnapshotManager: snapshotManager,
			DataDir:         cfg.server.dataDir,
			WikiVersion:     Version,
			WriteGate:       writeGate,
			AuthService:     w.AuthService(),
			Stores:          w.StateStores(),
			Reloadables:     []settings.Reloadable{w.BrandingService(), publicAccessService, tocDisplayService},
			UserResolver:    w.UserResolver(),
			TriggerResync: func() {
				// The restored content has nothing to do with the vectors built
				// before it: drop them, then let the resync rebuild (FR-604).
				w.ForgetSemanticIndex()
				w.TriggerResyncAsync()
			},
			MaxUploadSizeBytes: restoreUploadMaxSize,
		})
		// Registered after w.Close, so it runs before it (cleanups are
		// reversed): an in-flight restore must finish before AuthService's
		// user/session stores get closed out from under it during shutdown.
		cleanups = append(cleanups, restoreManager.Wait)
		w.SetRestoreRoutes(wikirestore.NewRoutes(restoreManager, w.AuthService()))
	}

	router := httpinternal.NewRouter(w.Registrars(), w.FrontendConfig(), httpinternal.RouterOptions{
		PublicAccess:            publicAccessService,
		AlwaysShowToc:           tocDisplayService,
		EditorLimit:             cfg.auth.editorLimit,
		InjectCodeInHeader:      cfg.frontend.injectCodeInHeader,
		CustomStylesheet:        cfg.frontend.customStylesheet,
		AllowInsecure:           cfg.server.allowInsecure,
		HideLinkMetadataSection: cfg.frontend.hideLinkMetadataSection,
		AccessTokenTimeout:      cfg.auth.accessTokenTimeout,
		RefreshTokenTimeout:     cfg.auth.refreshTokenTimeout,
		AuthDisabled:            cfg.auth.disableAuth,
		BasePath:                basePath,
		MaxAssetUploadSizeBytes: maxAssetUploadSize,
		EnableRevision:          cfg.frontend.enableRevision,
		EnableLinkRefactor:      cfg.frontend.enableLinkRefactor,
		EnableAPIKeyManagement:  cfg.frontend.enableAPIKeyManagement,
		Metrics:                 metrics,
		GitBackupEnabled:        backupManager.Enabled(),
		GitBackupEnvManaged:     backupManager.EnvManaged(),
		GitBackupConfigured:     backupManager.Configured(),
		SnapshotEnabled:         cfg.backup.snapshot,
		SMTPEnabled:             smtpEnabled,
		TOTPAvailable:           w.TOTPService() != nil,
		HTTPRemoteUser: httpinternal.HTTPRemoteUserConfig{
			Enabled:         cfg.proxy.enableHTTPRemoteUser,
			HeaderName:      cfg.proxy.httpRemoteUserHeader,
			AutoCreate:      cfg.proxy.enableHTTPRemoteUserAutoCreate,
			EmailHeaderName: cfg.proxy.httpRemoteUserEmailHeader,
			DefaultRole:     cfg.proxy.httpRemoteUserDefaultRole,
			TrustedProxies:  trustedProxies,
			UserService:     w.UserService,
		},
		APIKeyService:     w.APIKeyService(),
		DisableRequestLog: cfg.server.disableRequestLog,
		UserManagementURL: cfg.proxy.userManagementURL,
		DefaultLanguage:   cfg.frontend.defaultLanguage,
		LoginURL:          cfg.proxy.loginURL,
		LogoutURL:         logoutURL,
		WriteGate:         writeGate,
	})

	server := &http.Server{
		Handler:           router.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	reloadSignals := make(chan os.Signal, 1)
	notifyReloadSignals(reloadSignals)
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, syscall.SIGINT, syscall.SIGTERM)

	// Booting the wiki has no cancellation points, so a ctx cancelled during
	// setup is honoured here rather than mid-boot: the caller gets the error
	// instead of a server it has already given up on.
	if err := ctx.Err(); err != nil {
		return abort(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	instance := &Instance{listener: listener, wiki: w, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(instance.done)
		defer signal.Stop(reloadSignals)
		defer signal.Stop(shutdownSignals)
		defer func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
			closeWiki()
			if cleanup != nil {
				cleanup()
			}
			// After the wiki is closed and the socket is gone: only then is the
			// directory free for a new instance (FR-101).
			releaseWikiLock(lock)
		}()
		instance.serveErr = serveWithLifecycle(runCtx, server, listener, cleanup, w.TriggerResyncAsync, reloadSignals, shutdownSignals)
	}()

	return instance, nil
}

// controlCapabilities lists what this build's control channel implements, in
// FR-122b's vocabulary. It is the one place to update when a method lands: the
// status advertises exactly this list.
var controlCapabilities = []string{"auth.login", "page.get", "page.apply"}

// removeStaleControlDescriptor deletes a descriptor left behind by a run that is
// gone, so a client does not wait on a socket nobody serves (FR-106).
func removeStaleControlDescriptor(dataDir string) {
	path := filepath.Join(dataDir, "control.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Default().Warn("could not remove a stale control descriptor", "path", path, "error", err)
	}
}

// stateDBPaths returns the paths of every state database other than users.db
// that a snapshot captures (see restore.StateDBFiles).
func stateDBPaths(dataDir string) []string {
	files := restore.StateDBFiles()
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, filepath.Join(dataDir, f))
	}
	return paths
}

// buildPublicAccessService decides whether "public mode" is pinned by
// environment configuration or toggleable at runtime from Settings.
//
// It is env-managed when --public-access / LEAFWIKI_PUBLIC_ACCESS was supplied
// explicitly, or when --disable-auth forces public mode on (both cases keep
// today's behaviour and get a status-only Settings view). Otherwise it is
// settings-managed, reading its initial value from
// <data-dir>/public-access.json (absent ⇒ disabled).
func buildPublicAccessService(cfg *serverConfig) (*publicaccess.Service, error) {
	if cfg.auth.publicAccessExplicit || cfg.auth.disableAuth {
		return publicaccess.NewEnvManaged(cfg.auth.publicAccess || cfg.auth.disableAuth), nil
	}
	svc, err := publicaccess.NewSettingsManaged(cfg.server.dataDir)
	if err != nil {
		return nil, failf("Failed to load public-access configuration", err)
	}
	return svc, nil
}

func startMetricsServer(metrics *httpmetrics.HTTPMetrics, host, port string) (func(), string, error) {
	listenAddr := host + ":" + port
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, "", err
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.HTTPHandler())

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	slog.Default().Info("Starting metrics server", "address", listener.Addr().String())

	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return
		}
		slog.Default().Error("metrics server stopped unexpectedly", "error", err)
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				slog.Default().Error("failed to close metrics server", "error", errors.Join(err, closeErr))
				return
			}
			slog.Default().Error("failed to shut down metrics server gracefully", "error", err)
		}
	}, listener.Addr().String(), nil
}

func listenOnUnixSocket(socketPath string) (net.Listener, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("unix sockets are not supported on windows")
	}
	if err := removeStaleUnixSocket(socketPath); err != nil {
		return nil, err
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0660); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, err
	}

	return listener, nil
}

func removeStaleUnixSocket(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("unix socket path already exists and is not a socket: %s", socketPath)
		}
		return os.Remove(socketPath)
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func parseLogFormat(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "text":
		return "text", true
	case "json":
		return "json", true
	}
	return "", false
}

func parseBool(s string) (bool, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "true", "1", "yes", "y", "on":
		return true, true
	case "false", "0", "no", "n", "off":
		return false, true
	}

	return false, false
}

func parseByteSize(raw string) (int64, error) {
	size, err := humanize.ParseBytes(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("invalid byte size %q: %w", raw, err)
	}
	if size == 0 {
		return 0, fmt.Errorf("byte size must be greater than zero, got %q", raw)
	}
	if size > math.MaxInt64 {
		return 0, fmt.Errorf("byte size %q is too large", raw)
	}
	return int64(size), nil
}

func validateHTTPRemoteUserConfig(enabled bool, trustedProxyIPsRaw string) error {
	if !enabled {
		return nil
	}
	hasTrustedProxy := false
	for _, entry := range strings.Split(trustedProxyIPsRaw, ",") {
		if strings.TrimSpace(entry) != "" {
			hasTrustedProxy = true
			break
		}
	}
	if !hasTrustedProxy {
		return fmt.Errorf("--trusted-proxy-ips is required when --enable-http-remote-user is set. Set it using --trusted-proxy-ips or LEAFWIKI_TRUSTED_PROXY_IPS")
	}
	return nil
}

// validateHTTPRemoteUserAutoCreateConfig guards the auto-create opt-in: it requires
// reverse-proxy auth to already be enabled, and forbids "admin" as the default role so
// a forged or misrouted proxy header can never mint an admin account by itself. An
// operator who wants remote-provisioned admins can promote a user manually afterward.
func validateHTTPRemoteUserAutoCreateConfig(autoCreateEnabled, remoteUserEnabled bool, defaultRole string) error {
	if !autoCreateEnabled {
		return nil
	}
	if !remoteUserEnabled {
		return fmt.Errorf("--enable-http-remote-user-auto-create requires --enable-http-remote-user to also be set")
	}
	if !auth.IsValidRole(defaultRole) {
		return fmt.Errorf("--http-remote-user-default-role %q is not a valid role", defaultRole)
	}
	if defaultRole == auth.RoleAdmin {
		return fmt.Errorf("--http-remote-user-default-role must not be %q; promote auto-created users manually instead", auth.RoleAdmin)
	}
	return nil
}

// validateSMTPConfig fails fast at startup, mirroring
// validateHTTPRemoteUserConfig, rather than silently disabling the feature or
// leaving it half-configured until the first send attempt fails: a bad
// --public-url in particular would only surface once already embedded in an
// email that's gone out.
func validateSMTPConfig(host, from, publicURL, security string) error {
	if host == "" {
		return nil
	}
	if from == "" {
		return fmt.Errorf("--smtp-from is required when --smtp-host is set")
	}
	if publicURL == "" {
		return fmt.Errorf("--public-url is required when --smtp-host is set")
	}
	u, err := url.Parse(publicURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("--public-url must be an absolute http(s):// URL, got %q", publicURL)
	}
	switch email.Security(security) {
	case email.SecurityNone, email.SecurityStartTLS, email.SecurityTLS:
	default:
		return fmt.Errorf("--smtp-security must be one of none, starttls, tls, got %q", security)
	}
	return nil
}

// resolveLogoutURL resolves --logout-url/LEAFWIKI_LOGOUT_URL, falling back to
// the deprecated --http-remote-user-logout-url/LEAFWIKI_HTTP_REMOTE_USER_LOGOUT_URL
// when the new option isn't set. usedDeprecated tells the caller whether to log
// a deprecation warning.
func resolveLogoutURL(logoutURL, deprecated string) (resolved string, usedDeprecated bool) {
	if logoutURL != "" {
		return logoutURL, false
	}
	if deprecated == "" {
		return "", false
	}
	return deprecated, true
}

// validateGitBackupRemote checks the git backup remote URL and that credentials
// matching its transport are configured: HTTP(S) remotes authenticate with a
// username + password/token (e.g. a repo-scoped access token), SSH remotes with
// a private key. An empty remote means local-only backup and needs no
// credentials at all.
func validateGitBackupRemote(remote, sshKey, sshKeyPath, httpUsername, httpPassword string) error {
	return backup.ValidateRemoteCredentials(remote, sshKey, sshKeyPath, httpUsername, httpPassword)
}

// buildBackupManager wires up the git backup Manager in one of two modes:
//   - env-managed: --git-backup / LEAFWIKI_GIT_BACKUP is set. Config comes from
//     flags/env and the settings UI is status-only (historical behaviour).
//   - settings-managed: otherwise. Config lives in <data-dir>/git-backup.json,
//     written by admins via /settings/backup; the SSH key and HTTP password are
//     encrypted at rest with a key derived from the JWT secret (plaintext under
//     --disable-auth, where there is no such secret).
func buildBackupManager(cfg *serverConfig) (*backup.Manager, error) {
	rootDir := filepath.Join(cfg.server.dataDir, "root")
	assetsDir := filepath.Join(cfg.server.dataDir, "assets")

	if !cfg.backup.gitBackup {
		var box *sharedcrypto.SecretBox
		if cfg.auth.jwtSecret != "" {
			if key, err := sharedcrypto.DeriveKey([]byte(cfg.auth.jwtSecret), backup.CredentialsKeyInfo); err == nil {
				box, _ = sharedcrypto.NewSecretBox(key)
			}
		}
		return backup.NewSettingsManager(backup.NewConfigStore(cfg.server.dataDir, box), rootDir, assetsDir)
	}

	if cfg.backup.gitBackupSSHKey != "" && os.Getenv("LEAFWIKI_GIT_BACKUP_SSH_KEY") == "" {
		slog.Warn("SSH private key passed via --git-backup-ssh-key flag is visible in process listings; prefer the LEAFWIKI_GIT_BACKUP_SSH_KEY environment variable")
	}
	if cfg.backup.gitBackupHTTPPassword != "" && os.Getenv("LEAFWIKI_GIT_BACKUP_HTTP_PASSWORD") == "" {
		slog.Warn("HTTP password passed via --git-backup-http-password flag is visible in process listings; prefer the LEAFWIKI_GIT_BACKUP_HTTP_PASSWORD environment variable")
	}
	if strings.HasPrefix(strings.ToLower(cfg.backup.gitBackupRemote), "http://") {
		slog.Warn("--git-backup-remote uses plain http://; git backup credentials and wiki content are transmitted unencrypted — use https:// unless the remote is on a trusted network")
	}
	repo, err := backup.Init(backup.Config{
		Enabled:           true,
		RootDir:           rootDir,
		AssetsDir:         assetsDir,
		Path:              cfg.backup.gitBackupPath,
		AuthorName:        cfg.backup.gitBackupAuthorName,
		AuthorEmail:       cfg.backup.gitBackupAuthorEmail,
		RemoteURL:         cfg.backup.gitBackupRemote,
		Branch:            cfg.backup.gitBackupBranch,
		SSHKeyPath:        cfg.backup.gitBackupSSHKeyPath,
		SSHKey:            cfg.backup.gitBackupSSHKey,
		SSHKnownHostsPath: cfg.backup.gitBackupSSHKnownHosts,
		HTTPUsername:      cfg.backup.gitBackupHTTPUsername,
		HTTPPassword:      cfg.backup.gitBackupHTTPPassword,
		Interval:          cfg.backup.gitBackupInterval,
	})
	if err != nil {
		return nil, err
	}
	return backup.NewEnvManager(repo, backup.NewScheduler(repo)), nil
}

func validateRedirectURL(flagName, url string) error {
	if url == "" {
		return nil
	}
	lower := strings.ToLower(url)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return fmt.Errorf("--%s must start with http:// or https://", flagName)
	}
	return nil
}

func validateListenConfig(unixSocket string, hostSet, portSet bool) error {
	if unixSocket == "" {
		return nil
	}
	if hostSet || portSet {
		return fmt.Errorf("--unix-socket cannot be used together with --host or --port")
	}
	return nil
}

// serveWithLifecycle runs the HTTP server while handling live reload signals,
// graceful shutdown signals, and cancellation of ctx (which is how an instance
// stops without a signal). reload is non-blocking (fires a goroutine internally).
func serveWithLifecycle(
	ctx context.Context,
	server *http.Server,
	listener net.Listener,
	cleanup func(),
	reload func(),
	reloadSignals, shutdownSignals <-chan os.Signal,
) error {
	serveErrCh := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErrCh <- err
	}()

	stopReloads := make(chan struct{})
	var shuttingDown atomic.Bool
	var reloadWG sync.WaitGroup
	reloadWG.Add(1)
	go func() {
		defer reloadWG.Done()
		for {
			select {
			case <-stopReloads:
				return
			case sig, ok := <-reloadSignals:
				if !ok {
					return
				}
				if shuttingDown.Load() {
					return
				}
				slog.Default().Info("reload signal received: reloading from filesystem", "signal", sig.String())
				reload()
			}
		}
	}()

	stopReloader := sync.OnceFunc(func() {
		close(stopReloads)
	})
	defer stopReloader()

	waitForReloader := sync.OnceFunc(func() {
		reloadWG.Wait()
	})
	defer waitForReloader()

	// shutdown stops serving and releases the listener; it is shared by the
	// signal path and the cancelled-context path.
	shutdown := func(msg string, attrs ...any) error {
		slog.Default().Info(msg, attrs...)
		shuttingDown.Store(true)
		stopReloader()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		err := server.Shutdown(shutdownCtx)
		cancel()
		if err != nil {
			closeErr := server.Close()
			if closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				err = errors.Join(err, closeErr)
			}
		}

		if cleanup != nil {
			cleanup()
		}

		waitForReloader()
		serveErr := <-serveErrCh
		if err != nil {
			return err
		}
		return serveErr
	}

	for {
		select {
		case err := <-serveErrCh:
			return err
		case <-ctx.Done():
			return shutdown("shutdown requested: shutting down server")
		case sig, ok := <-shutdownSignals:
			if !ok {
				shutdownSignals = nil
				continue
			}

			return shutdown("shutdown signal received: shutting down server", "signal", sig.String())
		}
	}
}

// NormalizeBasePath normalizes the base path to the form "/mypath" (no trailing slash).
// Accepts "mypath", "/mypath", "/mypath/", etc. Returns "" for root. The desktop
// launcher uses the same function for its URLs (C-6).
func NormalizeBasePath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "/")
	if s == "" {
		return ""
	}
	return "/" + s
}
