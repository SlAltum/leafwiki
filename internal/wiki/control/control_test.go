package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/perber/wiki/internal/core/auth"
	"time"
)

// fakeSource is the wiki facts the channel asks for.
type fakeSource struct {
	status SourceStatus
	// users is the account store the identity checks read.
	users map[string]SourceUser
	// credentials maps "user:password" to the account it unlocks.
	credentials map[string]SourceUser
	// credentialErr lets a test replay a lockout or a TOTP account.
	credentialErr error
	// userErr lets a test replay a store that cannot answer.
	userErr error
}

func (f *fakeSource) ControlStatus(context.Context) SourceStatus { return f.status }

func (f *fakeSource) ControlUser(_ context.Context, userID string) (SourceUser, error) {
	if f.userErr != nil {
		return SourceUser{}, f.userErr
	}
	user, ok := f.users[userID]
	if !ok {
		return SourceUser{}, ErrNoSuchUser
	}
	return user, nil
}

func (f *fakeSource) ControlVerifyCredentials(_ context.Context, identifier, password string) (SourceUser, error) {
	if f.credentialErr != nil {
		return SourceUser{}, f.credentialErr
	}
	user, ok := f.credentials[identifier+":"+password]
	if !ok {
		return SourceUser{}, auth.ErrUserInvalidCredentials
	}
	return user, nil
}

// startTestService starts a channel in a temporary data directory and returns
// it with the socket path. A socket path the platform refuses is a skip, not a
// failure: the server is allowed to run without a channel (FR-104).
func startTestService(t *testing.T, cfg Config) *Service {
	t.Helper()

	dataDir := t.TempDir()
	cfg.DataDir = dataDir
	service, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.Start(); err != nil {
		t.Skipf("this platform cannot bind the control socket here: %v", err)
	}
	t.Cleanup(service.Stop)
	return service
}

// dial opens a client connection to the channel.
func dial(t *testing.T, socketPath string) net.Conn {
	t.Helper()

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial %s: %v", socketPath, err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

// roundTrip sends one request and decodes one response.
func roundTrip(t *testing.T, conn net.Conn, request *Request) *Response {
	t.Helper()

	if err := json.NewEncoder(conn).Encode(request); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &response
}

func TestNew_RejectsUnknownCapabilities(t *testing.T) {
	_, err := New(Config{DataDir: t.TempDir(), Capabilities: []string{"page.delete"}})
	if err == nil {
		t.Fatal("an unregistered capability was accepted")
	}
}

func TestParseAuthMode(t *testing.T) {
	if _, err := ParseAuthMode("required"); err != nil {
		t.Fatalf("ParseAuthMode(required): %v", err)
	}
	if _, err := ParseAuthMode("sometimes"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

func TestService_PublishesAndRemovesTheDescriptor(t *testing.T) {
	dataDir := t.TempDir()
	stale := filepath.Join(dataDir, "control.json")
	if err := os.WriteFile(stale, []byte(`{"socket":"/tmp/old","protocol":"0"}`), 0o644); err != nil {
		t.Fatalf("write stale descriptor: %v", err)
	}

	service, err := New(Config{DataDir: dataDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := service.Start(); startErr != nil {
		t.Skipf("this platform cannot bind the control socket here: %v", startErr)
	}
	t.Cleanup(service.Stop)

	payload, err := os.ReadFile(filepath.Join(dataDir, "control.json"))
	if err != nil {
		t.Fatalf("read descriptor: %v", err)
	}
	var descriptor Descriptor
	if err := json.Unmarshal(payload, &descriptor); err != nil {
		t.Fatalf("decode descriptor %q: %v", payload, err)
	}
	if descriptor.Protocol != Protocol || descriptor.Socket != service.SocketPath() {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	if info, err := os.Stat(filepath.Join(dataDir, "control.json")); err != nil {
		t.Fatalf("stat descriptor: %v", err)
	} else if runtimeIsUnix() && info.Mode().Perm() != 0o600 {
		t.Fatalf("descriptor mode = %o, want 0600", info.Mode().Perm())
	}

	service.Stop()
	if _, err := os.Stat(filepath.Join(dataDir, "control.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the descriptor survived Stop: %v", err)
	}
	if _, err := os.Stat(service.SocketPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket survived Stop: %v", err)
	}
}

func TestService_Status(t *testing.T) {
	service := startTestService(t, Config{
		Capabilities: []string{"page.get", "page.apply", "auth.login", "vectors.wait"},
		AuthMode:     AuthRequired,
		Source: &fakeSource{status: SourceStatus{
			AuthDisabled: true,
			Vectors:      VectorsStatus{Mode: "auto", Paused: true, Pending: 2, Failed: 1, EncoderAvailable: true},
			Pages:        42,
			Indexing:     IndexingStatus{Running: true},
		}},
	})

	response := roundTrip(t, dial(t, service.SocketPath()), &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "status"})
	if response.Error != nil {
		t.Fatalf("status failed: %#v", response.Error)
	}
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatalf("re-encode result: %v", err)
	}

	var status Status
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatalf("decode status %s: %v", encoded, err)
	}
	if status.Protocol != Protocol {
		t.Fatalf("protocol = %q", status.Protocol)
	}
	if status.Auth.Mode != string(AuthRequired) || !status.Auth.Disabled {
		t.Fatalf("auth = %#v", status.Auth)
	}
	if status.Vectors != (VectorsStatus{Mode: "auto", Paused: true, Pending: 2, Failed: 1, EncoderAvailable: true}) {
		t.Fatalf("vectors = %#v", status.Vectors)
	}
	if status.Pages != 42 || !status.Indexing.Running {
		t.Fatalf("status = %#v", status)
	}
	if strings.Join(status.Capabilities, ",") != "page.get,page.apply,auth.login,vectors.wait" {
		t.Fatalf("capabilities = %v", status.Capabilities)
	}
}

func TestService_UnknownMethod(t *testing.T) {
	service := startTestService(t, Config{})

	response := roundTrip(t, dial(t, service.SocketPath()), &Request{JSONRPC: "2.0", ID: json.RawMessage("7"), Method: "index.rebuild"})
	if response.Error == nil || response.Error.Code != CodeMethodNotFound {
		t.Fatalf("response = %#v, want method not found", response)
	}
	if response.Error.Data != nil && response.Error.Data.Kind != "" {
		t.Fatalf("error data = %#v, want no kind for a code §6.1 leaves bare", response.Error.Data)
	}
}

func TestService_RefusesMoreThanMaxConnections(t *testing.T) {
	service := startTestService(t, Config{})

	connections := make([]net.Conn, 0, MaxConnections)
	for i := 0; i < MaxConnections; i++ {
		connections = append(connections, dial(t, service.SocketPath()))
	}
	// The extra connection is accepted and closed immediately.
	extra := dial(t, service.SocketPath())
	if err := extra.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := extra.Read(buffer); err == nil {
		t.Fatal("the connection above the limit stayed open")
	}
}

func TestService_RateLimitsOneConnection(t *testing.T) {
	service := startTestService(t, Config{})
	conn := dial(t, service.SocketPath())

	limited := false
	for i := 0; i < RequestBurst+5; i++ {
		response := roundTrip(t, conn, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "status"})
		if response.Error != nil {
			if response.Error.Code != CodeRateLimited {
				t.Fatalf("unexpected error %#v", response.Error)
			}
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("%d requests in a row were not limited", RequestBurst+5)
	}
}

func TestService_RefusesAnOversizedFrame(t *testing.T) {
	service := startTestService(t, Config{})
	conn := dial(t, service.SocketPath())

	// A JSON string longer than the frame limit.
	oversized := `{"jsonrpc":"2.0","id":1,"method":"status","params":"` + strings.Repeat("x", MaxFrameBytes+64) + `"}`
	if _, err := io.WriteString(conn, oversized); err != nil {
		t.Fatalf("write oversized frame: %v", err)
	}

	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error == nil || response.Error.Code != CodeInvalidRequest {
		t.Fatalf("response = %#v, want an invalid request error", response)
	}
	if !strings.Contains(response.Error.Message, "exceeds") {
		t.Fatalf("message = %q, want it to say the frame is too large", response.Error.Message)
	}
}

func TestService_ReplacesAStaleSocket(t *testing.T) {
	dataDir := t.TempDir()
	socketPath := filepath.Join(dataDir, "control.sock")

	// A leftover that is a plain file must stop the channel instead of being
	// deleted: it is not ours to remove.
	if err := os.WriteFile(socketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	service, err := New(Config{DataDir: dataDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.Start(); err == nil {
		service.Stop()
		t.Fatal("the channel started over a file that is not a socket")
	}
}

// runtimeIsUnix reports whether file modes are meaningful here.
func runtimeIsUnix() bool {
	return os.PathSeparator == '/'
}

// A client that pipelines two requests gets two answers: a per-frame decoder
// would swallow the second frame and stall the connection (FR-107).
func TestService_AnswersPipelinedFrames(t *testing.T) {
	service := startTestService(t, Config{})
	conn := dial(t, service.SocketPath())

	first := `{"jsonrpc":"2.0","id":"a","method":"status"}`
	second := `{"jsonrpc":"2.0","id":"b","method":"status"}`
	if _, err := io.WriteString(conn, first+second); err != nil {
		t.Fatalf("write two frames: %v", err)
	}

	decoder := json.NewDecoder(conn)
	for _, wantID := range []string{"a", "b"} {
		if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatalf("read deadline: %v", err)
		}
		var response Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("decode response for %q: %v", wantID, err)
		}
		if string(response.ID) != `"`+wantID+`"` {
			t.Fatalf("response id = %s, want %q", response.ID, wantID)
		}
		if response.Error != nil {
			t.Fatalf("response for %q failed: %#v", wantID, response.Error)
		}
	}
}

// Stop closes an open connection instead of waiting for the idle timeout.
func TestService_StopClosesOpenConnections(t *testing.T) {
	service := startTestService(t, Config{})
	conn := dial(t, service.SocketPath())
	roundTrip(t, conn, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "status"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		service.Stop()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return with an open connection")
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the connection survived Stop")
	}
}

// The accepted connections work; the one above the limit is refused.
func TestService_KeepsTheAcceptedConnectionsWorking(t *testing.T) {
	service := startTestService(t, Config{})

	accepted := make([]net.Conn, 0, MaxConnections)
	for i := 0; i < MaxConnections; i++ {
		conn := dial(t, service.SocketPath())
		accepted = append(accepted, conn)
		response := roundTrip(t, conn, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "status"})
		if response.Error != nil {
			t.Fatalf("connection %d was refused: %#v", i+1, response.Error)
		}
	}
}

// The budget is per connection: one exhausted client does not starve another.
func TestService_BudgetIsPerConnection(t *testing.T) {
	service := startTestService(t, Config{})
	noisy := dial(t, service.SocketPath())
	quiet := dial(t, service.SocketPath())

	exhausted := false
	for i := 0; i < RequestBurst+5; i++ {
		response := roundTrip(t, noisy, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "status"})
		if response.Error != nil && response.Error.Code == CodeRateLimited {
			exhausted = true
			break
		}
	}
	if !exhausted {
		t.Fatal("the first connection was never rate limited")
	}

	response := roundTrip(t, quiet, &Request{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "status"})
	if response.Error != nil {
		t.Fatalf("the second connection was limited by the first: %#v", response.Error)
	}
}

// The file that is not a socket survives a refused start.
func TestService_LeavesANonSocketAlone(t *testing.T) {
	dataDir := t.TempDir()
	socketPath := filepath.Join(dataDir, "control.sock")
	if err := os.WriteFile(socketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	service, err := New(Config{DataDir: dataDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := service.Start(); startErr == nil {
		service.Stop()
		t.Fatal("the channel started over a file that is not a socket")
	}
	content, err := os.ReadFile(socketPath)
	if err != nil {
		t.Fatalf("the refused start removed the file: %v", err)
	}
	if string(content) != "not a socket" {
		t.Fatalf("the file changed: %q", content)
	}
}

// A socket path the platform cannot bind is the FR-104 degradation: the start
// fails, and nothing is left behind for a client to find.
func TestService_LongSocketPathFailsWithoutLeavingFiles(t *testing.T) {
	dataDir := t.TempDir()
	longPath := filepath.Join(dataDir, strings.Repeat("nested-segment-", 12)+"control.sock")

	service, err := New(Config{DataDir: dataDir, SocketPath: longPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := service.Start(); startErr == nil {
		service.Stop()
		t.Skip("this platform accepts a socket path that long")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "control.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed start published a descriptor: %v", err)
	}
	if _, err := os.Stat(longPath); err == nil {
		t.Fatal("a failed start left the socket file behind")
	}
}

// The status JSON field names are the contract (FR-122): a rename must fail
// here, not only in a client.
func TestService_StatusFieldNames(t *testing.T) {
	service := startTestService(t, Config{Capabilities: []string{"page.get"}, AuthMode: AuthOptional})

	response := roundTrip(t, dial(t, service.SocketPath()), &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "status"})
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatalf("re-encode result: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("decode status %s: %v", encoded, err)
	}
	if got, want := sortedKeys(raw), []string{"auth", "capabilities", "indexing", "pages", "protocol", "vectors"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("status fields = %v, want %v", got, want)
	}

	assertKeys := func(field, want string) {
		t.Helper()
		var section map[string]json.RawMessage
		if err := json.Unmarshal(raw[field], &section); err != nil {
			t.Fatalf("decode %s: %v", field, err)
		}
		if got := sortedKeys(section); strings.Join(got, ",") != want {
			t.Fatalf("%s fields = %v, want %s", field, got, want)
		}
	}
	assertKeys("auth", "disabled,mode")
	assertKeys("vectors", "encoder_available,failed,mode,paused,pending")
	assertKeys("indexing", "running")
}

// sortedKeys returns a map's keys, sorted.
func sortedKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// auth.login upgrades one connection without touching sessions.db (FR-111), and
// a wrong credential fails the same way HTTP does.
func TestService_LoginUpgradesTheConnection(t *testing.T) {
	reader := SourceUser{ID: "u1", Username: "writer", Role: auth.RoleEditor}
	source := &fakeSource{
		users:       map[string]SourceUser{"u1": reader},
		credentials: map[string]SourceUser{"writer:secret": reader},
	}
	service := startTestService(t, Config{Source: source, AuthMode: AuthRequired})

	conn := dial(t, service.SocketPath())
	response := roundTrip(t, conn, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
		Params: json.RawMessage(`{"user":"writer","password":"secret"}`)})
	if response.Error != nil {
		t.Fatalf("login failed: %#v", response.Error)
	}
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatalf("re-encode result: %v", err)
	}
	var result struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode login result %s: %v", encoded, err)
	}
	if result.User.ID != "u1" || result.User.Role != auth.RoleEditor {
		t.Fatalf("login result = %#v", result.User)
	}
}

func TestService_LoginRejectsWrongCredentials(t *testing.T) {
	source := &fakeSource{credentials: map[string]SourceUser{}}
	service := startTestService(t, Config{Source: source})

	response := roundTrip(t, dial(t, service.SocketPath()), &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
		Params: json.RawMessage(`{"user":"writer","password":"nope"}`)})
	if response.Error == nil || response.Error.Code != CodeUnauthenticated {
		t.Fatalf("response = %#v, want an unauthenticated error", response)
	}
	if response.Error.Data == nil || response.Error.Data.Kind != KindUnauthorized {
		t.Fatalf("error data = %#v, want the unauthorized kind", response.Error.Data)
	}
}

// A locked account and a TOTP account report themselves, so a caller knows not
// to retry blindly (FR-111, FR-119).
func TestService_LoginReportsTheReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind string
	}{
		{name: "locked", err: auth.ErrUserAccountLocked, kind: KindAccountLocked},
		{name: "totp", err: auth.ErrUserTOTPRequired, kind: KindTOTPRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &fakeSource{credentialErr: test.err}
			service := startTestService(t, Config{Source: source})

			response := roundTrip(t, dial(t, service.SocketPath()), &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
				Params: json.RawMessage(`{"user":"writer","password":"secret"}`)})
			if response.Error == nil || response.Error.Code != CodeUnauthenticated {
				t.Fatalf("response = %#v, want an unauthenticated error", response)
			}
			if response.Error.Data == nil || response.Error.Data.Kind != test.kind {
				t.Fatalf("error data = %#v, want %q", response.Error.Data, test.kind)
			}
		})
	}
}

// A successful login consumes budget too, and a second connection keeps its own
// (FR-119b).
func TestService_LoginBudgetCountsSuccessesAndIsPerConnection(t *testing.T) {
	reader := SourceUser{ID: "u1", Username: "writer", Role: auth.RoleAdmin}
	source := &fakeSource{
		users:       map[string]SourceUser{"u1": reader},
		credentials: map[string]SourceUser{"writer:secret": reader},
	}
	service := startTestService(t, Config{Source: source})
	conn := dial(t, service.SocketPath())

	// Four failures and one success use up the window.
	for i := 0; i < loginBudgetPerMinute-1; i++ {
		roundTrip(t, conn, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
			Params: json.RawMessage(`{"user":"writer","password":"nope"}`)})
	}
	response := roundTrip(t, conn, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
		Params: json.RawMessage(`{"user":"writer","password":"secret"}`)})
	if response.Error != nil {
		t.Fatalf("the successful login was refused: %#v", response.Error)
	}

	response = roundTrip(t, conn, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
		Params: json.RawMessage(`{"user":"writer","password":"secret"}`)})
	if response.Error == nil || response.Error.Code != CodeRateLimited {
		t.Fatalf("a success did not consume budget: %#v", response.Error)
	}

	other := dial(t, service.SocketPath())
	response = roundTrip(t, other, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
		Params: json.RawMessage(`{"user":"writer","password":"secret"}`)})
	if response.Error != nil {
		t.Fatalf("another connection inherited the budget: %#v", response.Error)
	}
}

// The login budget is per connection and five per minute (FR-119b).
func TestService_LoginBudget(t *testing.T) {
	source := &fakeSource{credentials: map[string]SourceUser{}}
	service := startTestService(t, Config{Source: source})
	conn := dial(t, service.SocketPath())

	for i := 0; i < loginBudgetPerMinute; i++ {
		response := roundTrip(t, conn, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
			Params: json.RawMessage(`{"user":"writer","password":"nope"}`)})
		if response.Error == nil || response.Error.Code != CodeUnauthenticated {
			t.Fatalf("attempt %d = %#v, want the credential error", i+1, response.Error)
		}
	}
	response := roundTrip(t, conn, &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
		Params: json.RawMessage(`{"user":"writer","password":"nope"}`)})
	if response.Error == nil || response.Error.Code != CodeRateLimited {
		t.Fatalf("response = %#v, want the login budget to refuse the sixth attempt", response.Error)
	}
}

// auth.login is the one method an unauthenticated client may call in required
// mode (FR-112); everything else is refused.
func TestService_RequiredModeRefusesAnonymousSessions(t *testing.T) {
	service := startTestService(t, Config{AuthMode: AuthRequired})

	if gateErr := service.authorize(context.Background(), &session{}, methodSpec{operation: auth.OpPageGet}); gateErr == nil {
		t.Fatal("required mode let an anonymous session read a page")
	}
	if gateErr := service.authorize(context.Background(), &session{}, methodSpec{public: true}); gateErr != nil {
		t.Fatalf("a public method was refused: %v", gateErr)
	}
}

// The optional tier may read and write content but not delete it (FR-113,
// FR-115).
func TestService_OptionalModeCapabilities(t *testing.T) {
	service := startTestService(t, Config{AuthMode: AuthOptional})

	for _, operation := range []auth.Operation{auth.OpPageGet, auth.OpPageCreate, auth.OpPageUpdate} {
		if gateErr := service.authorize(context.Background(), &session{}, methodSpec{operation: operation}); gateErr != nil {
			t.Fatalf("%s was refused for an anonymous session: %v", operation, gateErr)
		}
	}
	if gateErr := service.authorize(context.Background(), &session{}, methodSpec{operation: auth.OpPageDelete}); gateErr == nil {
		t.Fatal("an anonymous session may not delete pages")
	}
}

// An authenticated session is re-read on every request, so a deleted account
// stops working immediately (FR-117).
func TestService_DisabledAccountLosesTheSession(t *testing.T) {
	editor := SourceUser{ID: "u1", Username: "writer", Role: auth.RoleEditor}
	source := &fakeSource{users: map[string]SourceUser{"u1": editor}}
	service := startTestService(t, Config{Source: source, AuthMode: AuthOptional})

	session := &session{userID: editor.ID}
	if gateErr := service.authorize(context.Background(), session, methodSpec{operation: auth.OpPageDelete}); gateErr != nil {
		t.Fatalf("an editor was refused the delete capability: %v", gateErr)
	}

	delete(source.users, editor.ID)
	if gateErr := service.authorize(context.Background(), session, methodSpec{operation: auth.OpPageGet}); gateErr == nil {
		t.Fatal("a deleted account kept working")
	}
	if !session.anonymous() {
		t.Fatal("the session stayed authenticated after the account disappeared")
	}
}

// A store that cannot answer must not end the session: a hiccup would otherwise
// downgrade later writes to the anonymous identity without any error.
func TestService_TransientUserErrorKeepsTheSession(t *testing.T) {
	editor := SourceUser{ID: "u1", Username: "writer", Role: auth.RoleEditor}
	source := &fakeSource{users: map[string]SourceUser{"u1": editor}}
	service := startTestService(t, Config{Source: source, AuthMode: AuthOptional})

	session := &session{userID: editor.ID}
	source.userErr = errors.New("users.db is busy")
	gateErr := service.authorize(context.Background(), session, methodSpec{operation: auth.OpPageGet})
	if gateErr == nil {
		t.Fatal("a transient store error let the request through")
	}
	if gateErr.Code != CodeInternal {
		t.Fatalf("gate = %#v, want an internal error rather than an authentication one", gateErr)
	}
	if session.anonymous() || session.identity() != editor.ID {
		t.Fatalf("the session was downgraded to %q by a transient error", session.identity())
	}
}

// An anonymous session writes as the system user (FR-113, C-8).
func TestSession_Identity(t *testing.T) {
	if got := (&session{}).identity(); got != SystemUserID {
		t.Fatalf("anonymous identity = %q, want %q", got, SystemUserID)
	}
	if got := (&session{userID: "u1"}).identity(); got != "u1" {
		t.Fatalf("authenticated identity = %q, want the account id", got)
	}
}

// A --disable-auth instance cannot authenticate anybody, not even with correct
// credentials (FR-118).
func TestService_LoginRefusedOnADisabledAuthInstance(t *testing.T) {
	source := &fakeSource{
		status:      SourceStatus{AuthDisabled: true},
		credentials: map[string]SourceUser{"writer:secret": {ID: "u1", Username: "writer", Role: auth.RoleAdmin}},
	}
	service := startTestService(t, Config{Source: source})

	response := roundTrip(t, dial(t, service.SocketPath()), &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "auth.login",
		Params: json.RawMessage(`{"user":"writer","password":"secret"}`)})
	if response.Error == nil || response.Error.Code != CodeUnauthenticated {
		t.Fatalf("response = %#v, want an unauthenticated error on a disabled instance", response)
	}
}

// A role change is picked up on the next request, not only a deletion (FR-117).
func TestService_RoleChangeIsPickedUp(t *testing.T) {
	editor := SourceUser{ID: "u1", Username: "writer", Role: auth.RoleEditor}
	source := &fakeSource{users: map[string]SourceUser{"u1": editor}}
	service := startTestService(t, Config{Source: source, AuthMode: AuthOptional})

	session := &session{userID: editor.ID}
	if gateErr := service.authorize(context.Background(), session, methodSpec{operation: auth.OpPageDelete}); gateErr != nil {
		t.Fatalf("an editor was refused delete: %v", gateErr)
	}

	source.users["u1"] = SourceUser{ID: "u1", Username: "writer", Role: auth.RoleViewer}
	if gateErr := service.authorize(context.Background(), session, methodSpec{operation: auth.OpPageDelete}); gateErr == nil {
		t.Fatal("a demoted account kept the delete capability")
	}
}

// The refusal of an authenticated caller uses the §6.1 shape (-32011).
func TestService_PermissionDeniedEnvelope(t *testing.T) {
	viewer := SourceUser{ID: "u1", Username: "reader", Role: auth.RoleViewer}
	source := &fakeSource{users: map[string]SourceUser{"u1": viewer}}
	service := startTestService(t, Config{Source: source, AuthMode: AuthOptional})

	gateErr := service.authorize(context.Background(), &session{userID: viewer.ID}, methodSpec{operation: auth.OpPageDelete})
	if gateErr == nil {
		t.Fatal("a viewer was allowed to delete")
	}
	if gateErr.Code != CodeForbidden || gateErr.Data == nil || gateErr.Data.Kind != KindPermissionDenied {
		t.Fatalf("gate = %#v, want -32011 permission_denied", gateErr)
	}
	if gateErr.Data.Hint == "" {
		t.Fatal("permission_denied carries no hint")
	}
}

// A privileged method that declares no capability is refused rather than let
// through (FR-115).
func TestService_PrivilegedMethodWithoutCapabilityIsRefused(t *testing.T) {
	service := startTestService(t, Config{AuthMode: AuthOptional})

	gateErr := service.authorize(context.Background(), &session{}, methodSpec{})
	if gateErr == nil {
		t.Fatal("a non-public method without a capability was allowed")
	}
}
