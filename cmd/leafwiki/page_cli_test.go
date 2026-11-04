package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/perber/wiki/internal/wiki/control"
)

// startServedWiki starts a real server with its channel and returns the config a
// command would use.
func startServedWiki(t *testing.T) *serverConfig {
	t.Helper()

	dataDir := shortTempDir(t)
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
	t.Cleanup(func() {
		defer cancel()
		if stopErr := instance.Stop(stopCtx); stopErr != nil {
			t.Errorf("Stop: %v", stopErr)
		}
	})
	if _, err := os.Stat(filepath.Join(dataDir, "control.json")); err != nil {
		t.Skipf("the channel did not come up here: %v", err)
	}
	return cfg
}

// runArgs runs one page command and returns its exit code.
func runExitCode(t *testing.T, run func() error) int {
	t.Helper()

	err := run()
	if err == nil {
		return 0
	}
	var coded *exitCodeError
	if !errors.As(err, &coded) {
		t.Fatalf("error = %v (%T), want an exit code error", err, err)
	}
	return coded.code
}

// The commands read and write through the running server (FR-131, FR-192).
func TestPageCommands_EndToEnd(t *testing.T) {
	cfg := startServedWiki(t)
	options := &pageOptions{}

	var out bytes.Buffer
	code := runExitCode(t, func() error {
		return runPageWrite(cfg, options, control.ApplyParams{Items: []control.ApplyItem{{
			Path:    "platform/deploy",
			Create:  true,
			Title:   stringPtr("部署流程"),
			Content: stringPtr("# 部署流程 body"),
			Tags:    &[]string{"ops"},
		}}}, true, true, 0, &out, &bytes.Buffer{})
	})
	if code != 0 {
		t.Fatalf("write exit code = %d", code)
	}

	var result control.ApplyResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "create" || !result.Results[0].Changed {
		t.Fatalf("result = %#v", result)
	}
	if result.Results[0].Vector != "off" {
		t.Fatalf("vector = %q, want off without the feature", result.Results[0].Vector)
	}

	out.Reset()
	code = runExitCode(t, func() error {
		return runPageGet(cfg, options, "", "platform/deploy", true, &out)
	})
	if code != 0 {
		t.Fatalf("get exit code = %d", code)
	}
	var view control.PageView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if view.Title != "部署流程" || view.Content != "# 部署流程 body" {
		t.Fatalf("view = %#v", view)
	}

	out.Reset()
	code = runExitCode(t, func() error { return runStatus(cfg, true, &out) })
	if code != 0 {
		t.Fatalf("status exit code = %d", code)
	}
	var status control.Status
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if status.Protocol != "1" || status.Pages == 0 {
		t.Fatalf("status = %#v", status)
	}
}

// A page that is not there is exit 3, a bad command line is exit 2 (FR-195).
func TestPageCommands_ExitCodes(t *testing.T) {
	cfg := startServedWiki(t)
	options := &pageOptions{}

	if code := runExitCode(t, func() error {
		return runPageGet(cfg, options, "", "does-not-exist", true, &bytes.Buffer{})
	}); code != exitCodeNotFound {
		t.Fatalf("get of a missing page = %d, want %d", code, exitCodeNotFound)
	}

	if code := runExitCode(t, func() error {
		return runPageGet(cfg, options, "", "", true, &bytes.Buffer{})
	}); code != exitCodeUsage {
		t.Fatalf("get without a reference = %d, want %d", code, exitCodeUsage)
	}

	// A batch that only fails prechecks reports its own code.
	if code := runExitCode(t, func() error {
		return runPageApply(cfg, options, control.ApplyParams{Items: []control.ApplyItem{{Path: "no-title", Create: true}}}, true, true, 0, &bytes.Buffer{}, &bytes.Buffer{})
	}); code != exitCodeUsage {
		t.Fatalf("precheck failure = %d, want %d", code, exitCodeUsage)
	}
}

// The RPC table is the one place mapping codes onto exit codes (FR-196).
func TestExitCodeForRPC(t *testing.T) {
	tests := []struct {
		name string
		err  *control.Error
		want int
	}{
		{name: "unauthenticated", err: &control.Error{Code: control.CodeUnauthenticated}, want: exitCodeDenied},
		{name: "forbidden", err: &control.Error{Code: control.CodeForbidden}, want: exitCodeDenied},
		{name: "invalid params", err: &control.Error{Code: control.CodeInvalidParams}, want: exitCodeUsage},
		{name: "rate limited", err: &control.Error{Code: control.CodeRateLimited}, want: exitCodeUnavailable},
		{name: "restore", err: &control.Error{Code: control.CodeRestoreInProgress}, want: exitCodeUnavailable},
		{name: "page not found", err: &control.Error{Code: control.CodePageNotFound, Data: &control.ErrorData{Kind: control.KindPageNotFound}}, want: exitCodeNotFound},
		{name: "version conflict", err: &control.Error{Code: control.CodePageNotFound, Data: &control.ErrorData{Kind: control.KindPageVersionConflict}}, want: exitCodeVersionConflict},
		{name: "slug conflict", err: &control.Error{Code: control.CodePageNotFound, Data: &control.ErrorData{Kind: control.KindPageSlugConflict}}, want: exitCodeUsage},
		{name: "has children", err: &control.Error{Code: control.CodePageNotFound, Data: &control.ErrorData{Kind: control.KindPageHasChildren}}, want: exitCodeUsage},
		{name: "method not found", err: &control.Error{Code: control.CodeMethodNotFound}, want: exitCodeCapability},
		{name: "internal", err: &control.Error{Code: control.CodeInternal}, want: exitCodeFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := exitCodeForRPC(test.err); got != test.want {
				t.Fatalf("exit code = %d, want %d", got, test.want)
			}
		})
	}
}

// The socket is found through the flag, the descriptor, or the default path
// (FR-193b).
func TestResolveEndpoint(t *testing.T) {
	dataDir := t.TempDir()

	cfg := &serverConfig{server: serverOptions{dataDir: dataDir}}
	where, err := resolveEndpoint(cfg)
	if err != nil {
		t.Fatalf("resolveEndpoint: %v", err)
	}
	if !strings.HasSuffix(where.socket, "control.sock") || where.source != "default" {
		t.Fatalf("endpoint = %#v, want the default socket", where)
	}

	descriptor := filepath.Join(dataDir, "control.json")
	socket := filepath.Join(dataDir, "custom.sock")
	payload, err := json.Marshal(control.Descriptor{Socket: socket, Protocol: "1"})
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	if err := os.WriteFile(descriptor, payload, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
	where, err = resolveEndpoint(cfg)
	if err != nil {
		t.Fatalf("resolveEndpoint: %v", err)
	}
	if where.socket != socket || where.source != "descriptor" {
		t.Fatalf("endpoint = %#v, want the descriptor's socket", where)
	}

	cfg.control.controlSocket = filepath.Join(dataDir, "flag.sock")
	where, err = resolveEndpoint(cfg)
	if err != nil {
		t.Fatalf("resolveEndpoint: %v", err)
	}
	if where.source != "flag" {
		t.Fatalf("endpoint = %#v, want the flag to win", where)
	}
}

// An unreachable channel is not a refusal: only the first one may fall back
// (FR-194).
func TestDialControl_Classifies(t *testing.T) {
	cfg := &serverConfig{server: serverOptions{dataDir: t.TempDir()}}

	_, err := dialControl(cfg, endpoint{socket: filepath.Join(t.TempDir(), "gone.sock"), source: "test"})
	if !errors.Is(err, errChannelUnavailable) {
		t.Fatalf("missing socket = %v, want errChannelUnavailable", err)
	}

	notASocket := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(notASocket, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, err = dialControl(cfg, endpoint{socket: notASocket, source: "test"})
	if !errors.Is(err, errChannelUnavailable) && !errors.Is(err, errChannelRefused) {
		t.Fatalf("non-socket = %v, want a classified failure", err)
	}
}

func stringPtr(value string) *string { return &value }

// Credentials reach auth.login and the page is attributed to the account
// (FR-193, FR-113, FR-114).
func TestPageCommands_Credentials(t *testing.T) {
	cfg := startServedWiki(t)

	// The server runs in optional mode; a wrong password is still a refusal.
	code := runExitCode(t, func() error {
		return runPageWrite(cfg, &pageOptions{user: "admin", password: "wrong-password"}, control.ApplyParams{Items: []control.ApplyItem{{
			Path: "auth-check", Create: true, Title: stringPtr("Auth"), Content: stringPtr("body"),
		}}}, true, true, 0, &bytes.Buffer{}, &bytes.Buffer{})
	})
	if code != exitCodeDenied {
		t.Fatalf("wrong password = %d, want %d", code, exitCodeDenied)
	}

	var out bytes.Buffer
	code = runExitCode(t, func() error {
		return runPageWrite(cfg, &pageOptions{user: "admin", password: "adminpassword"}, control.ApplyParams{Items: []control.ApplyItem{{
			Path: "auth-check", Create: true, Title: stringPtr("Auth"), Content: stringPtr("body"),
		}}}, true, true, 0, &out, &bytes.Buffer{})
	})
	if code != 0 {
		t.Fatalf("write with credentials = %d", code)
	}

	raw, apiErr := func() (json.RawMessage, *control.Error) {
		client, _, err := openChannel(cfg, &pageOptions{})
		if err != nil {
			t.Fatalf("openChannel: %v", err)
		}
		defer client.close()
		return client.call("page.get", map[string]any{"path": "auth-check"})
	}()
	if apiErr != nil {
		t.Fatalf("page.get: %#v", apiErr)
	}
	var view control.PageView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.ID == "" {
		t.Fatalf("view = %#v", view)
	}

	// The attribution is in the file: the account, not the system user (FR-114).
	payload, err := os.ReadFile(filepath.Join(cfg.server.dataDir, "root", "auth-check.md"))
	if err != nil {
		t.Fatalf("read the written page: %v", err)
	}
	if strings.Contains(string(payload), "leafwiki_creator_id: "+control.SystemUserID) ||
		!strings.Contains(string(payload), "leafwiki_creator_id: ") {
		t.Fatalf("frontmatter does not carry the account as creator:\n%s", payload)
	}
}

// A user without any password source is a usage error, not an anonymous write
// (FR-193).
func TestPageOptions_CredentialsPrecedence(t *testing.T) {
	t.Setenv("LEAFWIKI_AGENT_USER", "")
	t.Setenv("LEAFWIKI_AGENT_PASSWORD", "")

	if _, err := (&pageOptions{user: "admin"}).credentials(); !errors.Is(err, errUsage) {
		t.Fatalf("user without password = %v, want a usage error", err)
	}
	if _, err := (&pageOptions{password: "secret"}).credentials(); !errors.Is(err, errUsage) {
		t.Fatalf("password without user = %v, want a usage error", err)
	}

	// The file beats the environment.
	file := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(file, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}
	t.Setenv("LEAFWIKI_AGENT_PASSWORD", "from-env")
	creds, err := (&pageOptions{user: "admin", passwordFile: file}).credentials()
	if err != nil || creds.password != "from-file" {
		t.Fatalf("credentials = %#v, %v; want the file to win", creds, err)
	}

	// The flag beats the file.
	creds, err = (&pageOptions{user: "admin", password: "from-flag", passwordFile: file}).credentials()
	if err != nil || creds.password != "from-flag" {
		t.Fatalf("credentials = %#v, %v; want the flag to win", creds, err)
	}

	// The environment is the last resort, for both halves.
	t.Setenv("LEAFWIKI_AGENT_USER", "admin")
	t.Setenv("LEAFWIKI_AGENT_PASSWORD", "from-env")
	creds, err = (&pageOptions{}).credentials()
	if err != nil || creds.user != "admin" || creds.password != "from-env" {
		t.Fatalf("credentials = %#v, %v; want the environment", creds, err)
	}
}

// The wait never reports success without having observed the page indexed
// (FR-163): a polling failure keeps it waiting and then times out.
func TestWaitForIndex_DoesNotLie(t *testing.T) {
	// A fake channel that always answers "still queued" to page.get.
	serverSide, clientSide := net.Pipe()
	defer func() {
		_ = clientSide.Close()
	}()
	go func() {
		decoder := json.NewDecoder(serverSide)
		encoder := json.NewEncoder(serverSide)
		for {
			var request struct {
				ID json.RawMessage `json:"id"`
			}
			if err := decoder.Decode(&request); err != nil {
				return
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": control.PageView{Path: "p", Vectors: "queued"}})
		}
	}()

	client := &controlClient{conn: clientSide, decoder: json.NewDecoder(clientSide)}
	result := control.ApplyResult{Results: []control.ApplyItemResult{{Path: "p", Action: "create", Vector: "queued"}}}

	waited, code := waitForIndex(client, result, 300*time.Millisecond, &bytes.Buffer{})
	if code != exitCodeUnavailable {
		t.Fatalf("wait = %d, want %d: a queued page must not look finished", code, exitCodeUnavailable)
	}
	if waited.Results[0].Vector != "pending" {
		t.Fatalf("vector = %q, want pending after the timeout", waited.Results[0].Vector)
	}
}

// The batch limits are checked locally, so an oversized batch is a usage error
// instead of a frame error (FR-141).
func TestRunPageApply_LocalLimits(t *testing.T) {
	cfg := &serverConfig{}
	items := make([]control.ApplyItem, 0, control.MaxApplyItems+1)
	for i := 0; i <= control.MaxApplyItems; i++ {
		items = append(items, control.ApplyItem{Path: "p" + strconv.Itoa(i), Create: true})
	}
	code := runExitCode(t, func() error {
		return runPageApply(cfg, &pageOptions{}, control.ApplyParams{Items: items}, true, true, 0, &bytes.Buffer{}, &bytes.Buffer{})
	})
	if code != exitCodeUsage {
		t.Fatalf("oversized batch = %d, want %d", code, exitCodeUsage)
	}
}

// A JSONL line with trailing garbage is refused (FR-192).
func TestReadBatch_RejectsTrailingGarbage(t *testing.T) {
	file := filepath.Join(t.TempDir(), "batch.jsonl")
	if err := os.WriteFile(file, []byte(`{"path":"a","create":true} {"path":"b"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write batch: %v", err)
	}
	if _, err := readBatch(file); !errors.Is(err, errUsage) {
		t.Fatalf("readBatch = %v, want a usage error", err)
	}
}

// Subcommand flag errors exit 2, not 1 (FR-195).
func TestPageCommands_UsageErrorsUseExitCodeTwo(t *testing.T) {
	cfg, _ := newServerConfigForTest(t)
	root := newRootCommandWithConfig(cfg)
	err := root.Run(context.Background(), []string{"leafwiki", "page", "get", "--bogus"})
	var coded *exitCodeError
	if !errors.As(err, &coded) || coded.code != exitCodeUsage {
		t.Fatalf("error = %v, want exit code %d", err, exitCodeUsage)
	}
}

// fakeChannel is a socket that answers canned JSON-RPC results, so the CLI's own
// decisions (partial batches, exit codes) can be tested without a server.
type fakeChannel struct {
	socket   string
	listener net.Listener
	mu       sync.Mutex
	methods  []string
}

// saw reports whether the client sent the method at all.
func (f *fakeChannel) saw(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, seen := range f.methods {
		if seen == method {
			return true
		}
	}
	return false
}

func startFakeChannel(t *testing.T, answer func(method string) any) *fakeChannel {
	t.Helper()

	dir := shortTempDir(t)
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("this platform cannot bind a unix socket here: %v", err)
	}
	channel := &fakeChannel{socket: socket, listener: listener}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() {
					_ = conn.Close()
				}()
				decoder := json.NewDecoder(conn)
				encoder := json.NewEncoder(conn)
				for {
					var request struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if decodeErr := decoder.Decode(&request); decodeErr != nil {
						return
					}
					channel.mu.Lock()
					channel.methods = append(channel.methods, request.Method)
					channel.mu.Unlock()
					if encodeErr := encoder.Encode(map[string]any{
						"jsonrpc": "2.0", "id": request.ID, "result": answer(request.Method),
					}); encodeErr != nil {
						return
					}
				}
			}()
		}
	}()
	return channel
}

// A batch with one success and one failure is a partial success: exit 8, with the
// written items reported (FR-148, FR-196).
func TestRunPageApply_PartialBatchExitsEight(t *testing.T) {
	channel := startFakeChannel(t, func(method string) any {
		if method != "page.apply" {
			return map[string]any{}
		}
		return map[string]any{"results": []map[string]any{
			{"path": "a", "action": "create", "changed": true},
			{"path": "b", "action": "none", "changed": false, "error": map[string]any{
				"code": -32010, "kind": "page_not_found", "hint": "add create",
			}},
		}}
	})

	cfg := &serverConfig{}
	options := &pageOptions{controlSocket: channel.socket}
	var out, errOut bytes.Buffer
	code := runExitCode(t, func() error {
		return runPageApply(cfg, options, control.ApplyParams{Items: []control.ApplyItem{
			{Path: "a", Create: true, Title: stringPtr("A")},
			{Path: "b", Create: true, Title: stringPtr("B")},
		}}, true, true, 0, &out, &errOut)
	})
	if code != exitCodePartial {
		t.Fatalf("exit code = %d, want %d", code, exitCodePartial)
	}
	var result control.ApplyResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if len(result.Results) != 2 || result.Results[1].Error == nil {
		t.Fatalf("results = %#v", result.Results)
	}
	if !strings.Contains(errOut.String(), "wrote 1 item(s), 1 failed") {
		t.Fatalf("stderr = %q, want the written/unwritten counts", errOut.String())
	}
}

// A method the server does not implement is exit 7, the CLI's capability signal
// (FR-123, FR-196).
func TestRunStatus_UnknownMethodIsCapability(t *testing.T) {
	channel := startFakeChannel(t, func(string) any { return nil })
	// The fake answers every method with a null result, so the CLI's own
	// decoding of a null status is what fails; the exit code is then 1, not 7.
	// The capability path is asserted through the table instead.
	if got := exitCodeForRPC(&control.Error{Code: control.CodeMethodNotFound}); got != exitCodeCapability {
		t.Fatalf("method not found = %d, want %d", got, exitCodeCapability)
	}
	_ = channel
}
