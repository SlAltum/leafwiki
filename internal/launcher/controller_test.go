package launcher

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeServer struct {
	http    *http.Server
	trigger atomic.Int32
}

func (f *fakeServer) Stop(ctx context.Context) error {
	f.trigger.Add(0)
	return f.http.Shutdown(ctx)
}

func (f *fakeServer) TriggerResync() bool {
	f.trigger.Add(1)
	return true
}

func (f *fakeServer) ResyncStatus() ResyncStatus { return ResyncStatus{} }

// healthyStarter serves /api/health on the launcher's own listener, which is
// what the readiness probe talks to.
func healthyStarter(calls *atomic.Int32) StartServer {
	return func(ctx context.Context, argv []string, listener net.Listener) (Server, error) {
		calls.Add(1)
		mux := http.NewServeMux()
		mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		server := &http.Server{Handler: mux}
		go func() {
			_ = server.Serve(listener)
		}()
		return &fakeServer{http: server}, nil
	}
}

func failingStarter(calls *atomic.Int32, err error) StartServer {
	return func(context.Context, []string, net.Listener) (Server, error) {
		calls.Add(1)
		return nil, err
	}
}

func newTestController(t *testing.T, deps Deps) *Controller {
	t.Helper()

	if deps.DataDir == "" {
		deps.DataDir = t.TempDir()
	}
	if deps.Secrets.JWT == "" {
		deps.Secrets = Secrets{JWT: "jwt-secret", AdminPassword: "admin-password"}
	}
	if deps.CredState == 0 {
		deps.CredState = CredSeeded
	}
	if deps.OpenURL == nil {
		deps.OpenURL = func(string) error { return nil }
	}
	if deps.OpenDir == nil {
		deps.OpenDir = func(string) error { return nil }
	}
	if deps.Validate == nil {
		deps.Validate = func([]string) error { return nil }
	}
	deps.ProbeInterval = 2 * time.Millisecond
	deps.ProbeLimit = 2 * time.Second
	deps.JoinTimeout = 2 * time.Second

	return NewController(deps)
}

func waitForControllerState(t *testing.T, c *Controller, want RunState) Status {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		status := c.Status()
		if status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %v (%s), want %v", status.State, status.Detail, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestController_StartReachesRunningAndShutdownStops(t *testing.T) {
	var calls atomic.Int32
	dataDir := t.TempDir()
	c := newTestController(t, Deps{DataDir: dataDir, Start: healthyStarter(&calls)})

	c.Start()
	running := waitForControllerState(t, c, StateRunning)

	if running.Port == 0 {
		t.Fatal("running status has no port")
	}
	if !strings.Contains(running.URL, "/api/health") && !strings.HasPrefix(running.URL, "http://127.0.0.1:") {
		t.Fatalf("URL = %q, want a loopback address", running.URL)
	}
	if _, err := os.Stat(filepath.Join(dataDir, stateFileName)); err != nil {
		t.Fatalf("state file missing while running: %v", err)
	}

	if state := c.Shutdown(); state != StateStopped {
		t.Fatalf("Shutdown() = %v, want stopped", state)
	}
	if _, err := os.Stat(filepath.Join(dataDir, stateFileName)); !os.IsNotExist(err) {
		t.Fatalf("state file survived Shutdown (stat err = %v)", err)
	}
}

func TestController_FailureIsReportedAndRetryConverges(t *testing.T) {
	var calls atomic.Int32
	var failFirst atomic.Bool
	failFirst.Store(true)

	starter := func(ctx context.Context, argv []string, listener net.Listener) (Server, error) {
		if failFirst.CompareAndSwap(true, false) {
			return nil, errors.New("the database is corrupt")
		}
		return healthyStarter(&calls)(ctx, argv, listener)
	}

	c := newTestController(t, Deps{Start: starter})

	c.Start()
	failed := waitForControllerState(t, c, StateFailed)
	if failed.Detail == "" {
		t.Fatal("failure detail is empty, want the technical text")
	}

	// FR-113: the retry converges the failed attempt instead of stacking one.
	c.Start()
	waitForControllerState(t, c, StateRunning)

	if got := calls.Load(); got != 1 {
		t.Fatalf("server starts = %d, want exactly the successful one", got)
	}

	c.Shutdown()
}

func TestController_StartWhileStartingIsIgnored(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, Deps{Start: healthyStarter(&calls)})

	c.Start()
	c.Start()
	c.Start()
	waitForControllerState(t, c, StateRunning)

	if got := calls.Load(); got != 1 {
		t.Fatalf("server starts = %d, want 1", got)
	}
	c.Shutdown()
}

func TestController_StopThenRestartRebinds(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, Deps{Start: healthyStarter(&calls)})

	c.Start()
	first := waitForControllerState(t, c, StateRunning)

	c.Stop()
	waitForControllerState(t, c, StateStopped)

	c.Restart()
	second := waitForControllerState(t, c, StateRunning)

	if first.Port == 0 || second.Port == 0 {
		t.Fatalf("ports = %d then %d, want both set", first.Port, second.Port)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("server starts = %d, want 2 (one per start)", got)
	}
	c.Shutdown()
}

func TestController_PinnedPortInUseFailsWithAPortMessage(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer func() {
		_ = busy.Close()
	}()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	var calls atomic.Int32
	params := []Param{{Name: "port", Kind: KindString, Value: itoa(busyPort)}}
	c := newTestController(t, Deps{Params: params, Start: healthyStarter(&calls)})

	c.Start()
	failed := waitForControllerState(t, c, StateFailed)

	if !strings.Contains(failed.Failed, itoa(busyPort)) {
		t.Fatalf("message = %q, want it to name port %d", failed.Failed, busyPort)
	}
	if calls.Load() != 0 {
		t.Fatal("the server was started for a port that could not be bound")
	}
}

func TestController_ValidateChecksTheBuiltArgv(t *testing.T) {
	var seen []string
	c := newTestController(t, Deps{
		Start:    healthyStarter(new(atomic.Int32)),
		Validate: func(argv []string) error { seen = argv; return nil },
	})

	message, err := c.Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if message != Text.ConfigOK {
		t.Fatalf("message = %q, want %q", message, Text.ConfigOK)
	}
	if len(seen) < 5 || !strings.HasPrefix(seen[1], "--data-dir=") {
		t.Fatalf("argv = %v, want the SRS §6.7 prefix", seen)
	}

	rejecting := newTestController(t, Deps{
		Start:    healthyStarter(new(atomic.Int32)),
		Validate: func([]string) error { return errors.New("Invalid SMTP configuration") },
	})
	if _, err := rejecting.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want the parser's failure")
	}
}

func TestController_ForceQuitRemovesTheStateFile(t *testing.T) {
	dataDir := t.TempDir()
	c := newTestController(t, Deps{DataDir: dataDir, Start: healthyStarter(new(atomic.Int32))})

	c.Start()
	waitForControllerState(t, c, StateRunning)

	if err := c.ForceQuit(); err != nil {
		t.Fatalf("ForceQuit() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, stateFileName)); !os.IsNotExist(err) {
		t.Fatalf("state file survived ForceQuit (stat err = %v)", err)
	}
}

func TestController_RefreshBeforeRunningIsIdle(t *testing.T) {
	c := newTestController(t, Deps{Start: healthyStarter(new(atomic.Int32))})
	if status := c.Refresh(); status.State != RefreshIdle {
		t.Fatalf("Refresh() = %v, want idle before a server exists", status.State)
	}
}

func TestController_QuietlyIgnoresUnusedDeps(t *testing.T) {
	// The controller must not require a log writer to be wired: the window can
	// exist before the log sink does.
	c := newTestController(t, Deps{Start: healthyStarter(new(atomic.Int32))})
	if status := c.Status(); status.State != StateStopped {
		t.Fatalf("new controller state = %v, want stopped", status.State)
	}
	_ = slog.Default()
}
