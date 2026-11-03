//go:build launcher

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/unit"
	"github.com/urfave/cli/v3"

	"github.com/perber/wiki/internal/core/tools"
	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/launcher"
)

// The launcher's own environment variables (SRS §6.2). They never count as the
// "someone configured the server" signal that FR-102 describes.
const (
	envNoUI      = "LEAFWIKI_LAUNCHER_NO_UI"
	envNoBrowser = "LEAFWIKI_LAUNCHER_NO_BROWSER"
)

func init() {
	launchLauncherHook = launchIfEligible
}

// launcherInput is FR-101's decision input; a pure value so the 16 combinations
// can be tested without a display.
type launcherInput struct {
	TagPresent bool
	Args       []string
	Env        []string
	HasDisplay bool
}

// launcherDecision is the outcome of the entry gate (SRS §6.1).
type launcherDecision int

const (
	decisionServer launcherDecision = iota
	decisionLauncher
)

// ShouldLaunch decides between the window and the plain server path: all four
// conditions must hold (K1 build tag, K2 no arguments, K3 no non-empty
// LEAFWIKI_* for the server, K4 a graphical session).
func ShouldLaunch(in launcherInput) launcherDecision {
	if !in.TagPresent || len(in.Args) > 1 || !in.HasDisplay {
		return decisionServer
	}
	if serverEnvPresent(in.Env) {
		return decisionServer
	}
	return decisionLauncher
}

// serverEnvPresent reports whether any LEAFWIKI_* variable other than the
// launcher's own is set to a non-empty value (FR-102).
func serverEnvPresent(env []string) bool {
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		if !found || strings.TrimSpace(value) == "" {
			continue
		}
		if !strings.HasPrefix(key, "LEAFWIKI_") {
			continue
		}
		// Every LEAFWIKI_LAUNCHER_* variable belongs to the launcher itself and
		// must not count as "someone configured the server" (FR-102).
		if strings.HasPrefix(key, "LEAFWIKI_LAUNCHER_") {
			continue
		}
		return true
	}
	return false
}

// graphicalSession reports whether a window can exist on this machine
// (SRS §6.1 K4).
func graphicalSession() bool {
	switch runtime.GOOS {
	case "windows", "darwin":
		return true
	default:
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
}

// launcherRing keeps the log lines the window shows (FR-605).
var launcherRing *launcher.Ring

func launcherLogLines() []string {
	if launcherRing == nil {
		return nil
	}
	return launcherRing.Lines(200)
}

// launchIfEligible is the root command's hook: it returns handled=true when the
// window took over, and false to fall through to the server path.
func launchIfEligible(context.Context) (bool, error) {
	input := launcherInput{
		TagPresent: true,
		Args:       os.Args,
		Env:        os.Environ(),
		HasDisplay: graphicalSession(),
	}
	if ShouldLaunch(input) == decisionServer {
		return false, nil
	}

	if noUI := os.Getenv(envNoUI); noUI == "1" {
		return true, runDegraded()
	}
	return true, runLauncherWindow()
}

// runLauncherWindow is SRS §6.8: window first, then everything else. Every
// failure after the window exists goes to the failed state instead of ending
// the process (C-12).
func runLauncherWindow() error {
	exeDir, err := executableDir()
	if err != nil {
		showMessageBox(launcher.Text.AppTitle, err.Error())
		return nil
	}
	dataDir := filepath.Join(exeDir, "data")

	setupLauncherLogging(dataDir)

	ctrl, cleanup, err := buildController(dataDir)
	if err != nil {
		// Before there is a window a system dialog is the only place to say it
		// (FR-115): nothing has been shown yet.
		showMessageBox(launcher.Text.AppTitle, err.Error())
		return nil
	}
	defer cleanup()

	ui := newLauncherUI(ctrl, dataDir)

	// First launch starts the server and opens the browser once; a user-driven
	// stop or restart later does not reopen it (FR-105).
	ctrl.Start()

	// No system title bar: the window draws its own headbar (brand, tabs, the
	// three window buttons) and drags itself through system.ActionMove.
	window := new(app.Window)
	window.Option(
		app.Title(ctrl.Branding().Name),
		app.Size(unit.Dp(560), unit.Dp(420)),
		app.Decorated(false),
	)

	// The taskbar/Alt-Tab icon follows the wiki's logo, not the icon the
	// executable was built with (FR-110).
	applyWindowIcon(context.Background(), ctrl.Branding().Logo)
	startHeapProfile()

	runErr := ui.run(window)
	state := ctrl.Shutdown()
	if state == launcher.StateClosing {
		// The server did not stop in time: drop the advertisement so the next
		// start cannot mistake it for a live instance (FR-702).
		slog.Warn("force quitting with the server still stopping")
		_ = ctrl.ForceQuit()
	}
	return runErr
}

// runDegraded starts the server without a window and opens the browser
// (FR-115/FR-703).
func runDegraded() error {
	exeDir, err := executableDir()
	if err != nil {
		return err
	}
	dataDir := filepath.Join(exeDir, "data")
	setupLauncherLogging(dataDir)

	ctrl, cleanup, err := buildController(dataDir)
	if err != nil {
		return err
	}
	defer cleanup()

	ctrl.Start()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		status := ctrl.Status()
		if status.State == launcher.StateRunning {
			_ = ctrl.OpenWiki()
			break
		}
		if status.State == launcher.StateFailed {
			return errors.New(status.Detail)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// No window means no close button: stay until the process is asked to stop.
	select {}
}

// buildController runs SRS §6.8 steps 1-7 and returns the controller plus a
// release func for the data directory lock.
func buildController(dataDir string) (*launcher.Controller, func(), error) {
	cleanup := func() {}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, cleanup, fmt.Errorf("%s: %w", launcher.Text.UnwritableDataDir, err)
	}
	if err := probeWritable(dataDir); err != nil {
		return nil, cleanup, fmt.Errorf("%s: %w", launcher.Text.UnwritableDataDir, err)
	}

	lock, err := launcher.Acquire(dataDir)
	if err != nil {
		if errors.Is(err, launcher.ErrLocked) {
			return nil, cleanup, lockedError(dataDir)
		}
		return nil, cleanup, err
	}
	cleanup = func() {
		_ = lock.Release()
	}

	// Credentials live next to the program, so an existing LeafWiki folder -
	// exe, jwt.secret, admin.password, data/ - keeps working when the launcher
	// binary is dropped into it (SRS §6.10).
	credDir := filepath.Dir(dataDir)

	secrets := launcher.Secrets{}
	if secrets.JWT, err = launcher.EnsureJWT(credDir); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	password, credState, err := launcher.ResolveAdminPassword(credDir, dataDir)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	secrets.AdminPassword = password

	config, err := launcher.Load(dataDir, launcher.Keep)
	if err != nil {
		slog.Warn("launcher configuration is unreadable, using defaults", "error", err)
		config = launcher.DefaultConfig()
	}

	params := applySavedParams(launcher.Collect((&serverConfig{}).flags()), config.Params)

	// Semantic search is this build's reason to exist: a binary that carries the
	// encoder defaults to auto, unless the user picked a mode themselves. A
	// binary without the encoder must pin off - the server rejects anything
	// else (C-7).
	if _, chosen := config.Params["vectors"]; !chosen {
		if embed.EncoderAvailable() {
			params = forceParam(params, "vectors", "auto")
		} else {
			params = forceParam(params, "vectors", "off")
		}
	}

	branding := launcher.LoadBranding(dataDir, launcher.Text.AppTitle)
	slog.Info("launcher identity", "site", branding.Name, "custom_logo", branding.Logo != nil)

	controller := launcher.NewController(launcher.Deps{
		DataDir:            dataDir,
		Params:             params,
		Secrets:            secrets,
		CredState:          credState,
		AutoRefresh:        config.AutoRefresh,
		VectorsAvailable:   embed.EncoderAvailable(),
		VectorsLibrary:     packageFile(credDir, "onnxruntime.dll"),
		VectorsModelDir:    packageDir(credDir, "models"),
		SuppressAutoOpen:   os.Getenv(envNoBrowser) == "1",
		Start:              startServerForLauncher,
		Validate:           validateArgv,
		OpenURL:            openURLForLauncher,
		ResetAdminPassword: resetAdminPasswordForLauncher,
		OpenDir:            func(target string) error { return launcher.OpenDir(launcher.DefaultRunner, target) },
	})

	return controller, cleanup, nil
}

// resetAdminPasswordForLauncher regenerates the admin password with the server
// stopped (FR-205). The admin identity comes from the CLI defaults, so the
// launcher and the command line agree on which account is "the admin".
func resetAdminPasswordForLauncher(dataDir string) (string, error) {
	cfg, err := parseServerConfig([]string{
		"leafwiki",
		"--data-dir=" + dataDir,
		"--jwt-secret=unused-for-parsing",
		"--admin-password=unused-for-parsing",
	})
	if err != nil {
		return "", err
	}

	user, err := tools.ResetAdminPassword(dataDir, cfg.auth.adminUsername, cfg.auth.adminEmail)
	if err != nil {
		return "", err
	}
	return user.Password, nil
}

// openURLForLauncher opens the wiki in the default browser. The automation
// switch LEAFWIKI_LAUNCHER_NO_BROWSER=1 only suppresses the automatic open at
// startup; a click on "open the wiki" always works.
func openURLForLauncher(target string) error {
	return launcher.OpenURL(launcher.DefaultRunner, target)
}

// lockedError turns the locked branch into the sentence the user reads
// (SRS §6.4).
func lockedError(dataDir string) error {
	state, err := launcher.ReadState(dataDir)
	if err != nil {
		return errors.New(launcher.Text.AnotherInstance)
	}

	probe := func() bool {
		address := fmt.Sprintf("http://127.0.0.1:%d%s/api/health", state.Port, state.BasePath)
		client := &http.Client{Timeout: time.Second}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := client.Get(address)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return true
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		return false
	}

	switch launcher.DecideLocked(state, os.Getpid(), probe) {
	case launcher.HandOver:
		_ = launcher.OpenURL(launcher.DefaultRunner,
			fmt.Sprintf("http://127.0.0.1:%d%s", state.Port, state.BasePath))
		return nil
	default:
		return errors.New(launcher.Text.AnotherInstance)
	}
}

// probeWritable proves the data directory accepts writes before the window
// claims anything runs there (FR-704).
func probeWritable(dataDir string) error {
	path := filepath.Join(dataDir, ".write-test")
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		return err
	}
	return os.Remove(path)
}

func executableDir() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate the program: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Dir(path), nil
	}
	return filepath.Dir(resolved), nil
}

// packageFile returns an existing file next to the executable, or "".
func packageFile(dir, name string) string {
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}

// packageDir returns an existing directory next to the executable, or "".
func packageDir(dir, name string) string {
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return ""
	}
	return path
}

// forceParam pins one row to a value: it is a fact of this build, not a choice.
func forceParam(params []launcher.Param, name, value string) []launcher.Param {
	for i := range params {
		if params[i].Name == name {
			params[i].Value = value
			params[i].Default = value
			params[i].Changed = false
		}
	}
	return params
}

// applySavedParams copies the persisted values onto the freshly collected rows.
func applySavedParams(params []launcher.Param, saved map[string]string) []launcher.Param {
	for i := range params {
		value, ok := saved[params[i].Name]
		if !ok || !launcher.Editable(params[i].Name) {
			continue
		}
		params[i].Value = value
		params[i].Changed = value != params[i].Default
	}
	return params
}

// setupLauncherLogging installs the one handler chain the process logs through:
// stdout, the window ring and the rotating file all see the same records
// (FR-601, C-9).
func setupLauncherLogging(dataDir string) {
	sink, err := launcher.OpenFileSink(dataDir)
	if err != nil {
		slog.Error("cannot open the launcher log file", "error", err)
	}

	handler := launcher.NewHandler(nil, 2000, sink)
	launcherRing = handler.Ring()

	format, _ := parseLogFormat("text")
	SetupLoggerWith(os.Stdout, format, handler)

	if sink != nil {
		slog.Info("launcher log file", "path", sink.Path())
	}
}

// startServerForLauncher is the adapter between the launcher's policy and the
// server's own entry point: the argv is parsed by the real CLI, so flag
// handling exists once (C-6, FR-112).
func startServerForLauncher(ctx context.Context, argv []string, listener net.Listener) (launcher.Server, error) {
	cfg, err := parseServerConfig(argv)
	if err != nil {
		return nil, err
	}
	instance, err := runServerWithListener(ctx, cfg, listener)
	if err != nil {
		return nil, err
	}
	return &instanceServer{instance: instance}, nil
}

func validateArgv(argv []string) error {
	_, err := parseServerConfig(argv)
	return err
}

// parseServerConfig runs the CLI tree without starting anything (SRS §6.7's
// four takeovers: argv[0], Before, Action, ErrWriter).
func parseServerConfig(argv []string) (*serverConfig, error) {
	cfg := &serverConfig{}
	cmd := newRootCommandWithConfig(cfg)

	var diagnostics strings.Builder
	cmd.Before = func(ctx context.Context, _ *cli.Command) (context.Context, error) {
		return ctx, nil
	}
	cmd.Action = func(_ context.Context, parsed *cli.Command) error {
		return validateServerConfig(parsed, cfg)
	}
	cmd.Writer = io.Discard
	cmd.ErrWriter = &diagnostics

	if err := cmd.Run(context.Background(), argv); err != nil {
		return nil, fmt.Errorf("%s%s", diagnostics.String(), err)
	}
	return cfg, nil
}

// instanceServer adapts the in-process server to the launcher's interface.
type instanceServer struct {
	instance *Instance
}

func (s *instanceServer) Stop(ctx context.Context) error {
	return s.instance.Stop(ctx)
}

func (s *instanceServer) TriggerResync() bool {
	wiki := s.instance.Wiki()
	if wiki == nil {
		return false
	}
	if wiki.ResyncStatus().Running {
		return false
	}
	wiki.TriggerResyncAsync()
	return true
}

func (s *instanceServer) ResyncStatus() launcher.ResyncStatus {
	wiki := s.instance.Wiki()
	if wiki == nil {
		return launcher.ResyncStatus{}
	}
	status := wiki.ResyncStatus()
	return launcher.ResyncStatus{Running: status.Running, Done: status.Done, Error: status.Error}
}
