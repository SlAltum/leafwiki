package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/perber/wiki/internal/wiki/control"
)

// The offline path writes when nobody is serving, and leaves the index alone when
// it cannot update it (FR-171, FR-173b, FR-175).
func TestRunOffline_WritesWithoutAServer(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	var out, errOut bytes.Buffer
	handled, code, err := runOffline(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{
		Path: "offline/page", Create: true, Title: stringPtr("Offline"), Content: stringPtr("body"),
	}}}, true, &out, &errOut)
	if !handled {
		t.Fatalf("the offline path declined the command: %v", err)
	}
	if code != exitCodeOK {
		t.Fatalf("exit code = %d, error = %v", code, err)
	}
	if !strings.Contains(errOut.String(), "offline") {
		t.Fatalf("stderr = %q, want a note about writing offline", errOut.String())
	}

	written := filepath.Join(dataDir, "root", "offline", "page.md")
	if _, statErr := os.Stat(written); statErr != nil {
		t.Fatalf("the page was not written: %v", statErr)
	}

	// The index could not be updated (no vectors.db), so the result must not
	// claim it was.
	var result control.ApplyResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "create" {
		t.Fatalf("result = %#v", result)
	}
	if result.Results[0].Vector == "indexed" {
		t.Fatal("the offline run claimed the page was indexed without a vectors database")
	}
}

// A running marker turns the fallback into exit 75: the instance is supposed to
// be there, so "not reachable" is a lock problem, not an invitation to write
// behind its back (FR-194).
func TestRunOffline_RunningMarkerLocks(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}
	if err := os.WriteFile(filepath.Join(dataDir, "control.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	handled, code, _ := runOffline(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{Path: "x", Create: true}}}, true, &bytes.Buffer{}, &bytes.Buffer{})
	if !handled || code != exitCodeLocked {
		t.Fatalf("handled = %v, code = %d; want the lock code", handled, code)
	}
}

// A held wiki.lock is also exit 75, never a silent write (FR-171).
func TestRunOffline_LockedDataDir(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}
	lock, err := acquireTestWikiLock(dataDir)
	if err != nil {
		t.Fatalf("acquireTestWikiLock: %v", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			t.Errorf("release: %v", releaseErr)
		}
	}()

	handled, code, _ := runOffline(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{Path: "x", Create: true}}}, true, &bytes.Buffer{}, &bytes.Buffer{})
	if !handled || code != exitCodeLocked {
		t.Fatalf("handled = %v, code = %d; want the lock code", handled, code)
	}
}

// page delete goes through the same batch path (FR-146).
func TestPageCommands_Delete(t *testing.T) {
	cfg := startServedWiki(t)

	if code := runExitCode(t, func() error {
		return runPageWrite(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{
			Path: "doomed", Create: true, Title: stringPtr("Doomed"), Content: stringPtr("body"),
		}}}, true, true, 0, &bytes.Buffer{}, &bytes.Buffer{})
	}); code != 0 {
		t.Fatalf("create = %d", code)
	}

	// Deleting anonymously is refused with exit 5.
	if code := runExitCode(t, func() error {
		return runPageApply(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{Path: "doomed", Delete: true}}}, true, true, 0, &bytes.Buffer{}, &bytes.Buffer{})
	}); code != exitCodeDenied {
		t.Fatalf("anonymous delete = %d, want %d", code, exitCodeDenied)
	}

	// An editor may delete.
	var out bytes.Buffer
	if code := runExitCode(t, func() error {
		return runPageApply(cfg, &pageOptions{user: "admin", password: "adminpassword"}, control.ApplyParams{Items: []control.ApplyItem{{Path: "doomed", Delete: true}}}, true, true, 0, &out, &bytes.Buffer{})
	}); code != 0 {
		t.Fatalf("delete = %d", code)
	}
	var result control.ApplyResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if !result.Results[0].Irreversible {
		t.Fatalf("result = %#v, want an irreversible delete", result.Results[0])
	}
}

// A refused channel never falls back: only an unreachable one may (FR-194).
func TestRunPageApply_RefusalDoesNotFallBack(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	// A socket path that exists but is not a socket cannot be dialed; the
	// classification is exercised through the exit code the caller returns.
	code := runExitCode(t, func() error {
		return runPageApply(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{
			Path: "should-not-exist", Create: true, Title: stringPtr("No"), Content: stringPtr("body"),
		}}}, true, true, 0, &bytes.Buffer{}, &bytes.Buffer{})
	})
	// Unreachable and marker-less: the offline path handles it, so the page is
	// written. That is the positive half.
	if code != 0 {
		t.Fatalf("offline write = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "root", "should-not-exist.md")); err != nil {
		t.Fatalf("the offline write did not happen: %v", err)
	}

	// A refusal has to be reported as such, so build one: a held peer is not
	// available in-process, but exitCodeForClientError must not hand back the
	// fallback-eligible code for a refusal.
	if got := exitCodeForClientError(fmt.Errorf("%w: nope", errChannelRefused)); got != exitCodeDenied {
		t.Fatalf("refused = %d, want %d", got, exitCodeDenied)
	}
	if got := exitCodeForClientError(fmt.Errorf("%w: nope", errChannelUnavailable)); got != exitCodeUnavailable {
		t.Fatalf("unavailable = %d, want %d", got, exitCodeUnavailable)
	}
}

// An offline batch that fails keeps its own code and message (FR-194: the
// fallback must not re-label the failure).
func TestRunOffline_FailureKeepsItsCode(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	handled, code, err := runOffline(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{
		Path: "no-title", Create: true,
	}}}, true, &bytes.Buffer{}, &bytes.Buffer{})
	if !handled {
		t.Fatal("the offline path declined the command")
	}
	if code != exitCodeUsage {
		t.Fatalf("exit code = %d, want %d", code, exitCodeUsage)
	}
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("error = %v, want the precheck message rather than a dial error", err)
	}
}

// Offline --dry-run refuses instead of repairing files (FR-147: zero writes).
func TestRunOffline_DryRunNeedsTheServer(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	handled, code, err := runOffline(cfg, &pageOptions{}, control.ApplyParams{DryRun: true, Items: []control.ApplyItem{{Path: "x", Delete: true}}}, true, &bytes.Buffer{}, &bytes.Buffer{})
	if !handled {
		t.Fatal("the offline path declined the command")
	}
	if code != exitCodeUnavailable {
		t.Fatalf("exit code = %d, want %d", code, exitCodeUnavailable)
	}
	if err == nil {
		t.Fatal("the refusal carries no explanation")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "favorites.db")); statErr == nil {
		t.Fatal("an offline dry run created a database")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "schema.json")); statErr == nil {
		t.Fatal("an offline dry run wrote the tree")
	}
}

// An offline delete works when the caller authenticates (FR-174).
func TestRunOffline_DeleteWithCredentials(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}

	// First create the page offline, using an account: users.db has to exist, so
	// start a server once and stop it.
	startServerOnce(t, dataDir)

	handled, code, err := runOffline(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{
		Path: "offline-delete", Create: true, Title: stringPtr("Gone"), Content: stringPtr("body"),
	}}}, true, &bytes.Buffer{}, &bytes.Buffer{})
	if !handled || code != 0 {
		t.Fatalf("offline create = %v, %d, %v", handled, code, err)
	}

	// Without credentials the delete is refused (capability).
	handled, code, _ = runOffline(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{Path: "offline-delete", Delete: true}}}, true, &bytes.Buffer{}, &bytes.Buffer{})
	if !handled || code != exitCodeDenied {
		t.Fatalf("anonymous offline delete = %v, %d; want %d", handled, code, exitCodeDenied)
	}

	credentials := &pageOptions{user: "admin", password: "adminpassword"}
	handled, code, err = runOffline(cfg, credentials, control.ApplyParams{Items: []control.ApplyItem{{Path: "offline-delete", Delete: true}}}, true, &bytes.Buffer{}, &bytes.Buffer{})
	if !handled || code != 0 {
		t.Fatalf("offline delete with credentials = %v, %d, %v", handled, code, err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "root", "offline-delete.md")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the page survived the offline delete: %v", statErr)
	}
}

// startServerOnce creates and removes a running instance's data directory, so
// users.db exists for the offline credential check.
func startServerOnce(t *testing.T, dataDir string) {
	t.Helper()

	cfg, _ := newServerConfigForTest(t,
		"--jwt-secret=test-secret-for-the-agent-channel",
		"--admin-password=adminpassword",
		"--data-dir="+dataDir,
		"--snapshot=false",
	)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	instance, err := runServerWithListener(context.Background(), cfg, listener, mustWikiLock(t, dataDir))
	if err != nil {
		t.Fatalf("runServerWithListener: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := instance.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// The launcher marker also locks (FR-194).
func TestRunOffline_LauncherMarkerLocks(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}
	if err := os.WriteFile(filepath.Join(dataDir, "launcher.state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	handled, code, _ := runOffline(cfg, &pageOptions{}, control.ApplyParams{Items: []control.ApplyItem{{Path: "x", Create: true}}}, true, &bytes.Buffer{}, &bytes.Buffer{})
	if !handled || code != exitCodeLocked {
		t.Fatalf("handled = %v, code = %d; want the lock code", handled, code)
	}
}

// A typo of the page group is a usage error with nothing on stdout (FR-195).
func TestPageCommand_GroupUsage(t *testing.T) {
	cfg, _ := newServerConfigForTest(t)
	root := newRootCommandWithConfig(cfg)
	err := root.Run(context.Background(), []string{"leafwiki", "page", "delet"})
	var coded *exitCodeError
	if !errors.As(err, &coded) || coded.code != exitCodeUsage {
		t.Fatalf("error = %v, want exit code %d", err, exitCodeUsage)
	}
}

// The credential flags must reach the client: a delete without a login is refused
// by the server, and dropping the flags made every real CLI delete fail while the
// in-process tests (which set the struct directly) stayed green.
func TestPageDelete_CredentialFlagsReachTheClient(t *testing.T) {
	channel := startFakeChannel(t, func(method string) any {
		switch method {
		case "auth.login":
			return map[string]any{"id": "u1", "role": "admin"}
		case "page.apply":
			return map[string]any{"results": []map[string]any{
				{"path": "doomed", "action": "delete", "changed": true, "irreversible": true},
			}}
		default:
			return map[string]any{}
		}
	})

	cfg, _ := newServerConfigForTest(t)
	root := newRootCommandWithConfig(cfg)
	err := root.Run(context.Background(), []string{
		"leafwiki", "page", "delete", "doomed",
		"--user", "admin", "--password", "adminpassword",
		"--control-socket", channel.socket, "--no-wait", "--json",
	})
	if err != nil {
		t.Fatalf("delete through the command tree: %v", err)
	}
	if !channel.saw("auth.login") {
		t.Fatal("the CLI never sent auth.login: the credential flags did not reach the client")
	}
	if !channel.saw("page.apply") {
		t.Fatal("the CLI never sent page.apply")
	}
}

// A password file and the environment are the other two documented sources, in
// that order (FR-193).
func TestPageOptions_CredentialPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "pw")
	if err := os.WriteFile(file, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}

	t.Setenv("LEAFWIKI_AGENT_USER", "envuser")
	t.Setenv("LEAFWIKI_AGENT_PASSWORD", "from-env")

	fromFile := &pageOptions{user: "flaguser", passwordFile: file}
	creds, err := fromFile.credentials()
	if err != nil {
		t.Fatalf("credentials() error = %v", err)
	}
	if creds.user != "flaguser" || creds.password != "from-file" {
		t.Fatalf("creds = %q/%q, want the flag user and the file password", creds.user, creds.password)
	}

	fromEnv := &pageOptions{}
	creds, err = fromEnv.credentials()
	if err != nil {
		t.Fatalf("credentials() error = %v", err)
	}
	if creds.user != "envuser" || creds.password != "from-env" {
		t.Fatalf("creds = %q/%q, want the environment pair", creds.user, creds.password)
	}
}
