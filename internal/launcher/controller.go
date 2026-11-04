package launcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/perber/wiki/internal/core/shared/advisorylock"
	"github.com/perber/wiki/internal/core/shared/wikilock"
)

// State is one of the six states the window shows (FR-805).
type RunState string

const (
	StateStopped  RunState = "stopped"
	StateStarting RunState = "starting"
	StateRunning  RunState = "running"
	StateStopping RunState = "stopping"
	StateClosing  RunState = "closing"
	StateFailed   RunState = "failed"
)

// ResyncStatus is the refresh state the launcher reads, mirroring
// resync.JobStatus without depending on the wiki's job type.
type ResyncStatus struct {
	Running bool
	Done    bool
	Error   string
}

// Server is the running wiki as the launcher sees it; cmd/leafwiki adapts its
// Instance to this interface.
type Server interface {
	// Stop ends the server and waits for its resources to be released.
	Stop(ctx context.Context) error
	// TriggerResync starts a content reload, reporting whether it actually
	// started one (false when a reload is already in flight).
	TriggerResync() bool
	// ResyncStatus reports the reload's state.
	ResyncStatus() ResyncStatus
}

// StartServer starts the server on the listener the launcher bound. argv is the
// argument vector built from the parameter table, so the caller parses it with
// the real CLI instead of duplicating flag handling.
// StartServer starts the server on a listener the launcher owns. It takes the
// data-directory lock, which the server releases when it stops: the launcher takes
// it first, so its own writes into the data directory happen while it owns the
// directory (FR-101, FR-194).
type StartServer func(ctx context.Context, argv []string, listener net.Listener, lock *advisorylock.File) (Server, error)

// Deps are the controller's collaborators. Tests replace every one of them.
type Deps struct {
	DataDir string
	// Params are the rows of the advanced page, values already applied.
	Params []Param
	// Secrets are handed to the server, never shown (FR-204).
	Secrets     Secrets
	CredState   CredState
	AutoRefresh bool
	// ParamsDirty reports whether the table holds unsaved edits.
	Start func(ctx context.Context, argv []string, listener net.Listener, lock *advisorylock.File) (Server, error)
	// Validate check-parses argv without starting anything (FR-507).
	Validate func(argv []string) error
	// WriteAgentBrief writes the AGENT.md next to the executable; nil skips it.
	WriteAgentBrief func(exeDir, dataDir, url string) error
	OpenURL         func(target string) error
	OpenDir         func(target string) error
	// ResetAdminPassword regenerates the admin password and returns the new
	// one; it runs with the server stopped (FR-205).
	ResetAdminPassword func(dataDir string) (string, error)

	// VectorsAvailable reports whether this binary carries the semantic
	// encoder. Only then may the launcher default --vectors to auto; a build
	// without the tag rejects anything but off at startup (C-7).
	VectorsAvailable bool
	// VectorsLibrary and VectorsModelDir are the files the desktop package
	// ships next to the executable, handed to the server when the table does
	// not set them itself.
	VectorsLibrary  string
	VectorsModelDir string
	// SuppressAutoOpen keeps the first successful start from opening a browser.
	// It never affects the user's own "open the wiki" click.
	SuppressAutoOpen bool

	Host string
	Now  func() time.Time

	JoinTimeout   time.Duration
	ProbeInterval time.Duration
	ProbeLimit    time.Duration
}

func (d Deps) host() string {
	if d.Host == "" {
		return "127.0.0.1"
	}
	return d.Host
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d Deps) joinTimeout() time.Duration {
	if d.JoinTimeout <= 0 {
		return 10 * time.Second
	}
	return d.JoinTimeout
}

func (d Deps) probeInterval() time.Duration {
	if d.ProbeInterval <= 0 {
		return 200 * time.Millisecond
	}
	return d.ProbeInterval
}

func (d Deps) probeLimit() time.Duration {
	if d.ProbeLimit <= 0 {
		return 60 * time.Second
	}
	return d.ProbeLimit
}

// Status is everything the window draws, captured in one consistent snapshot.
type Status struct {
	State     RunState
	Port      int
	BasePath  string
	URL       string
	Account   string
	Password  string
	CredState CredState
	StartedAt time.Time
	// Failed is the sentence for the user; Detail the technical text (FR-701).
	Failed  string
	Detail  string
	Refresh RefreshStatus
	// NextStop reports that a port was saved and will be used next start.
	Params []Param
}

// Controller owns the server's lifecycle for the window. All exported methods
// are safe to call from the UI thread; anything that blocks runs in a goroutine
// and reports through Status (FR-104).
type Controller struct {
	deps Deps

	mu            sync.Mutex
	state         RunState
	failedMessage string
	detail        string
	port          int
	basePath      string
	startedAt     time.Time
	server        Server
	listener      net.Listener
	browserOpened bool
	params        []Param
	autoRefresh   bool
	tracker       *Tracker
	branding      Branding
	lastSnapshot  Snapshot
	stats         ContentStats
	statsLoaded   bool
	statsAt       time.Time
	pendingAt     time.Time
	pending       bool
	// join is the goroutine currently doing work; prevJoin the one before it,
	// which a retry has to wait for.
	join     chan struct{}
	prevJoin chan struct{}
}

// NewController builds the controller in the stopped state.
func NewController(deps Deps) *Controller {
	c := &Controller{
		deps:        deps,
		state:       StateStopped,
		params:      append([]Param(nil), deps.Params...),
		autoRefresh: deps.AutoRefresh,
	}
	c.basePath = NormalizeBasePath(paramValue(c.params, "base-path"))
	c.branding = LoadBranding(deps.DataDir, Text.AppTitle)
	if c.deps.WriteAgentBrief == nil {
		c.deps.WriteAgentBrief = launcherWriteAgentBrief
	}
	return c
}

// Status returns the current snapshot.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := Status{
		State:     c.state,
		Port:      c.port,
		BasePath:  c.basePath,
		Account:   Text.AccountName,
		CredState: c.deps.CredState,
		StartedAt: c.startedAt,
		Failed:    c.failedMessage,
		Detail:    c.detail,
		Params:    append([]Param(nil), c.params...),
	}
	if c.port != 0 {
		status.URL = c.urlLocked()
	}
	if c.deps.CredState != CredUnknown {
		status.Password = c.deps.Secrets.AdminPassword
	}
	if c.tracker != nil {
		status.Refresh = c.tracker.Status()
	}
	return status
}

// InState reports whether the controller is in state; the view uses it to
// enable buttons.
func (c *Controller) InState(states ...RunState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, state := range states {
		if c.state == state {
			return true
		}
	}
	return false
}

// Start runs SRS §6.8 steps 5-10 in the background. A start that is already in
// flight is ignored, so double clicks cannot start two servers (FR-113).
func (c *Controller) Start() {
	if !c.begin(StateStarting) {
		return
	}
	c.goroutine(c.runStart)
}

// Stop ends the server in the background; the process stays alive (FR-107).
func (c *Controller) Stop() {
	if !c.beginFrom(StateRunning, StateStarting, StateFailed) {
		return
	}
	c.setState(StateStopping)
	c.goroutine(func() { c.stopAndSettle() })
}

// Restart stops the server and starts it again on a freshly bound listener
// (FR-108). The data directory lock stays with this process.
func (c *Controller) Restart() {
	if !c.beginFrom(StateRunning, StateStarting, StateStopped, StateFailed) {
		return
	}
	c.setState(StateStopping)
	c.goroutine(func() {
		c.stopAndSettle()
		c.runStart()
	})
}

// OpenWiki starts the server when needed and opens the browser on it (FR-801).
func (c *Controller) OpenWiki() error {
	switch c.currentState() {
	case StateRunning:
		c.maybeAutoRefresh()
		return c.openBrowser()
	case StateStopped, StateFailed:
		c.Start()
		return nil
	default:
		return nil
	}
}

// OpenDataDir opens the data directory in the file manager (FR-803).
func (c *Controller) OpenDataDir() error {
	if err := c.deps.OpenDir(c.deps.DataDir); err != nil {
		return fmt.Errorf("%s: %w", Text.FailedOpenDir, err)
	}
	return nil
}

// Refresh triggers one content reload and follows it (FR-401).
func (c *Controller) Refresh() RefreshStatus {
	c.mu.Lock()
	tracker := c.tracker
	c.mu.Unlock()
	if tracker == nil {
		return RefreshStatus{State: RefreshIdle}
	}
	return tracker.Start()
}

// RecheckRefresh picks up a reload that outlived its poll window (FR-403).
func (c *Controller) RecheckRefresh() RefreshStatus {
	c.mu.Lock()
	tracker := c.tracker
	c.mu.Unlock()
	if tracker == nil {
		return RefreshStatus{State: RefreshIdle}
	}
	return tracker.Recheck()
}

// ResetAdminPassword stops the server, resets the admin password and starts it
// again (FR-205). The new password shows up in Status immediately.
func (c *Controller) ResetAdminPassword() error {
	if c.deps.ResetAdminPassword == nil {
		return errors.New("no password reset wired")
	}
	if c.deps.CredState == CredSeeded && c.deps.Secrets.AdminPassword == "" {
		return errors.New("no admin password known")
	}

	state := c.currentState()
	if state == StateRunning || state == StateStarting {
		c.setState(StateStopping)
		c.stopAndSettle()
	}

	password, err := c.deps.ResetAdminPassword(c.deps.DataDir)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.deps.Secrets.AdminPassword = password
	c.deps.CredState = CredFromFile
	c.mu.Unlock()

	c.Start()
	return nil
}

// MaybeAutoRefresh compares the content root with the previous focus-time
// snapshot and refreshes only when something changed (FR-405/406).
func (c *Controller) MaybeAutoRefresh() {
	c.maybeAutoRefresh()
}

// SetParams replaces the table rows after the advanced page edited them.
func (c *Controller) SetParams(params []Param) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.params = append([]Param(nil), params...)
}

// Params returns a copy of the current rows.
func (c *Controller) Params() []Param {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Param(nil), c.params...)
}

// Validate checks the current table without starting anything (FR-507/508).
func (c *Controller) Validate() (string, error) {
	if c.deps.Validate == nil {
		return "", errors.New("no parser wired")
	}
	argv := c.argvFor(c.boundPortOrFallback())
	if err := c.deps.Validate(argv); err != nil {
		return "", err
	}
	return Text.ConfigOK, nil
}

// Shutdown stops the server within the join budget and releases the state file;
// the window calls it when it closes (FR-106). It reports the state left behind.
func (c *Controller) Shutdown() RunState {
	c.setState(StateClosing)
	c.stopAndSettle()
	return c.currentState()
}

// ForceQuit drops the state file before the process exits, so the next start
// cannot mistake it for a live instance (FR-702).
func (c *Controller) ForceQuit() error {
	return RemoveState(c.deps.DataDir)
}

// Branding is the wiki's configured name and logo (FR-110).
func (c *Controller) Branding() Branding {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.branding
}

// Stats reports the content counts for the ticket, loaded once in the
// background so opening the window never waits on a directory walk.
func (c *Controller) Stats() ContentStats {
	c.mu.Lock()
	loaded, stats := c.statsLoaded, c.stats
	c.mu.Unlock()

	if loaded && (stats.Pages > 0 || stats.Assets > 0) {
		return stats
	}
	if loaded && time.Since(c.statsAt) < 5*time.Second {
		return stats
	}

	c.mu.Lock()
	c.statsLoaded = true
	c.statsAt = time.Now()
	go func() {
		computed := Stats(c.deps.DataDir)
		c.mu.Lock()
		c.stats = computed
		c.mu.Unlock()
	}()
	c.mu.Unlock()
	return stats
}

// PendingChanges reports whether the content root moved since the last check
// while auto-refresh is off. The answer is cached for a few seconds so the
// window can ask every frame without walking the tree every frame (FR-405).
func (c *Controller) PendingChanges() bool {
	c.mu.Lock()
	enabled := c.autoRefresh
	cachedAt, cached := c.pendingAt, c.pending
	c.mu.Unlock()

	if enabled {
		return false
	}
	if time.Since(cachedAt) < 3*time.Second {
		return cached
	}

	snapshot, err := TakeSnapshot(filepath.Join(c.deps.DataDir, "root"))
	if err != nil {
		return false
	}

	c.mu.Lock()
	changed := c.lastSnapshot != nil && Changed(c.lastSnapshot, snapshot)
	c.pending = changed
	c.pendingAt = time.Now()
	c.mu.Unlock()
	return changed
}

// Uptime is how long the current server has been running.
func (c *Controller) Uptime() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.startedAt.IsZero() || c.state != StateRunning {
		return 0
	}
	return c.deps.now().Sub(c.startedAt)
}

func (c *Controller) runStart() {
	c.converge()
	if c.currentState() == StateClosing {
		return
	}

	// The directory comes first: everything below writes into it, and a second
	// instance must fail before the launcher touches a live one (FR-101).
	if err := wikilock.EnsureDataDir(c.deps.DataDir); err != nil {
		c.fail(err)
		return
	}
	lock, err := wikilock.Acquire(c.deps.DataDir)
	if err != nil {
		c.fail(err)
		return
	}
	// The lock is released before the failure is reported, so a retry finds it
	// free; on success the server owns it until it stops.
	failLocked := func(startErr error) {
		wikilock.Release(lock)
		c.fail(startErr)
	}

	if err := EnsurePublicRead(c.deps.DataDir); err != nil {
		failLocked(err)
		return
	}

	fixed := 0
	if value := c.paramValue("port"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			fixed = parsed
		}
	}

	listener, port, err := Listen(c.deps.host(), c.rememberedPort(fixed))
	if err != nil && fixed == 0 && c.rememberedPort(0) != 0 {
		// The remembered random port was taken: pick a fresh one and update
		// what the window shows (SRS §6.5).
		listener, port, err = Repick(c.deps.host())
	}
	if err != nil {
		wikilock.Release(lock)
		c.failPort(err)
		return
	}

	c.mu.Lock()
	c.listener = listener
	c.port = port
	argv := c.argvLocked(port)
	c.basePath = NormalizeBasePath(paramValue(c.params, "base-path"))
	st := State{PID: os.Getpid(), Port: port, BasePath: c.basePath, StartedAt: StateTimestamp(c.deps.now())}
	c.mu.Unlock()

	// A state write failure is only a warning (SRS §6.8 step 7): the next
	// instance then simply finds no advertised port.
	_ = WriteState(c.deps.DataDir, st)

	ctx, cancel := context.WithCancel(context.Background())
	_ = cancel // the server's own Stop cancels its context

	server, err := c.deps.Start(ctx, argv, listener, lock)
	if err != nil {
		failLocked(err)
		return
	}

	c.mu.Lock()
	c.server = server
	c.mu.Unlock()

	if err := c.awaitHealthy(port); err != nil {
		c.fail(err)
		return
	}

	c.setRunning()

	if !c.browserOpened && !c.deps.SuppressAutoOpen {
		c.mu.Lock()
		c.browserOpened = true
		url := c.urlLocked()
		c.mu.Unlock()
		_ = c.deps.OpenURL(url)
	}
}

// converge releases whatever the previous start attempt left behind, so a retry
// never runs a second server next to a half-finished one (FR-113).
func (c *Controller) converge() {
	c.mu.Lock()
	server := c.server
	listener := c.listener
	joined := c.prevJoin
	c.prevJoin = nil
	c.server = nil
	c.listener = nil
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), c.deps.joinTimeout())
	defer cancel()

	if server != nil {
		_ = server.Stop(ctx)
	}
	if listener != nil {
		_ = listener.Close()
	}
	if joined != nil {
		select {
		case <-joined:
		case <-ctx.Done():
		}
	}
	_ = RemoveState(c.deps.DataDir)
}

func (c *Controller) stopAndSettle() {
	c.mu.Lock()
	server := c.server
	listener := c.listener
	c.server = nil
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), c.deps.joinTimeout())
	defer cancel()

	var stopErr error
	if server != nil {
		stopErr = server.Stop(ctx)
	}
	if listener != nil {
		_ = listener.Close()
	}
	_ = RemoveState(c.deps.DataDir)

	if errors.Is(stopErr, context.DeadlineExceeded) || errors.Is(context.Cause(ctx), context.DeadlineExceeded) || ctx.Err() != nil {
		// Still not settled: keep the window in "closing" and offer the
		// force-quit escape (FR-114/702).
		c.setState(StateClosing)
		return
	}

	c.mu.Lock()
	c.port = 0
	c.startedAt = time.Time{}
	c.tracker = nil
	c.mu.Unlock()
	c.setState(StateStopped)
}

func (c *Controller) awaitHealthy(port int) error {
	client := &http.Client{Timeout: time.Second}
	deadline := c.deps.now().Add(c.deps.probeLimit())

	for {
		status, err := c.fetchHealth(client, port)
		if err == nil && status == http.StatusOK {
			return nil
		}
		if c.deps.now().After(deadline) {
			if err != nil {
				return fmt.Errorf("the server did not answer on port %d: %w", port, err)
			}
			return fmt.Errorf("the server did not answer on port %d (status %d)", port, status)
		}
		time.Sleep(c.deps.probeInterval())
	}
}

func (c *Controller) fetchHealth(client *http.Client, port int) (int, error) {
	c.mu.Lock()
	basePath := c.basePath
	c.mu.Unlock()

	resp, err := client.Get(c.urlFor(port, basePath) + "/api/health")
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	return resp.StatusCode, nil
}

func (c *Controller) maybeAutoRefresh() {
	if !c.autoRefreshEnabled() {
		return
	}

	snapshot, err := TakeSnapshot(c.contentRoot())
	if err != nil {
		return
	}

	c.mu.Lock()
	previous := c.lastSnapshot
	c.lastSnapshot = snapshot
	c.mu.Unlock()

	if previous != nil && !Changed(previous, snapshot) {
		return
	}
	c.Refresh()
}

// SetAutoRefresh turns the focus-time snapshot comparison on or off (FR-405).
func (c *Controller) SetAutoRefresh(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.autoRefresh = enabled
}

// AutoRefresh reports the switch's current value.
func (c *Controller) AutoRefresh() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.autoRefresh
}

func (c *Controller) autoRefreshEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.autoRefresh
}

func (c *Controller) contentRoot() string {
	return c.deps.DataDir + string(os.PathSeparator) + "root"
}

func (c *Controller) openBrowser() error {
	c.mu.Lock()
	url := c.urlLocked()
	c.mu.Unlock()

	if err := c.deps.OpenURL(url); err != nil {
		return fmt.Errorf("%s: %w", Text.FailedOpenWiki, err)
	}
	return nil
}

func (c *Controller) urlLocked() string {
	return c.urlFor(c.port, c.basePath)
}

func (c *Controller) urlFor(port int, basePath string) string {
	return "http://" + net.JoinHostPort(c.deps.host(), strconv.Itoa(port)) + basePath
}

func (c *Controller) rememberedPort(fixed int) int {
	if fixed != 0 {
		return fixed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.port
}

func (c *Controller) boundPortOrFallback() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.port != 0 {
		return c.port
	}
	for _, param := range c.params {
		if param.Name == "port" {
			if parsed, err := strconv.Atoi(param.Value); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func (c *Controller) argvFor(port int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.argvLocked(port)
}

func (c *Controller) argvLocked(port int) []string {
	argv := BuildArgv(c.params, c.deps.Secrets, c.deps.DataDir, port)

	// The package's own encoder assets, unless the table overrides them.
	if c.deps.VectorsLibrary != "" && !c.paramSetLocked("vectors-library") {
		argv = append(argv, "--vectors-library="+c.deps.VectorsLibrary)
	}
	if c.deps.VectorsModelDir != "" && !c.paramSetLocked("vectors-model-dir") {
		argv = append(argv, "--vectors-model-dir="+c.deps.VectorsModelDir)
	}
	return argv
}

// paramSetLocked reports whether the table carries a value for name.
func (c *Controller) paramSetLocked(name string) bool {
	for _, param := range c.params {
		if param.Name == name && param.Value != "" {
			return true
		}
	}
	return false
}

func (c *Controller) paramValue(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return paramValue(c.params, name)
}

func paramValue(params []Param, name string) string {
	for _, param := range params {
		if param.Name == name {
			return param.Value
		}
	}
	return ""
}

func (c *Controller) setRunning() {
	c.mu.Lock()
	c.state = StateRunning
	c.failedMessage = ""
	c.detail = ""
	c.startedAt = c.deps.now()
	if c.server != nil {
		server := c.server
		c.tracker = NewTracker(TrackerOptions{
			Trigger:  func() bool { return server.TriggerResync() },
			Status:   server.ResyncStatus,
			Interval: 100 * time.Millisecond,
			Limit:    10 * time.Second,
		})
	}
	c.mu.Unlock()

	c.writeAgentBrief()

	if snapshot, err := TakeSnapshot(c.contentRoot()); err == nil {
		c.mu.Lock()
		c.lastSnapshot = snapshot
		c.mu.Unlock()
	}
}

func (c *Controller) fail(err error) {
	c.mu.Lock()
	c.state = StateFailed
	c.failedMessage = Text.StatusFailed
	c.detail = err.Error()
	c.mu.Unlock()
	_ = RemoveState(c.deps.DataDir)
}

func (c *Controller) failPort(err error) {
	var portErr *PortError
	if errors.As(err, &portErr) {
		c.mu.Lock()
		c.state = StateFailed
		c.failedMessage = fmt.Sprintf("端口 %d 被占用。%s", portErr.Port, Text.PortInUseDetail)
		c.detail = err.Error()
		c.mu.Unlock()
		_ = RemoveState(c.deps.DataDir)
		return
	}
	c.fail(err)
}

// begin moves to a state when the machine is not busy; it reports whether the
// caller may start the work.
func (c *Controller) begin(target RunState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case StateStarting, StateStopping, StateClosing:
		return false
	}
	c.state = target
	c.failedMessage = ""
	c.detail = ""
	return true
}

func (c *Controller) beginFrom(allowed ...RunState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, state := range allowed {
		if c.state == state {
			return true
		}
	}
	return false
}

func (c *Controller) setState(state RunState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
}

func (c *Controller) currentState() RunState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// goroutine runs work in the background and lets Shutdown/Stop wait for it
// (FR-114: no unjoined goroutines are left behind).
func (c *Controller) goroutine(work func()) {
	done := make(chan struct{})
	c.mu.Lock()
	c.prevJoin = c.join
	c.join = done
	c.mu.Unlock()

	go func() {
		defer close(done)
		work()
	}()
}

// NormalizeBasePath normalizes a base path to "/mypath", returning "" for the
// root. It is cmd/leafwiki's rule, shared so both sides agree (C-6).
func NormalizeBasePath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "/")
	if s == "" {
		return ""
	}
	return "/" + s
}

// writeAgentBrief publishes the AGENT.md an agent reads, with the address it just
// started on. A failure is not fatal: the wiki works without the file.
func (c *Controller) writeAgentBrief() {
	if c.deps.WriteAgentBrief == nil {
		return
	}
	c.mu.Lock()
	url := c.urlLocked()
	dataDir := c.deps.DataDir
	c.mu.Unlock()

	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = c.deps.WriteAgentBrief(filepath.Dir(exe), dataDir, url)
}
