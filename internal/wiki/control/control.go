package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The channel's contract numbers (docs/srs/agent-operations.srs.md FR-107,
// FR-108). They are constants rather than options: a client has to know them.
const (
	// Protocol is the wire version the descriptor advertises.
	Protocol = "1"
	// MaxConnections bounds concurrent clients.
	MaxConnections = 4
	// IdleTimeout closes a connection that sends nothing.
	IdleTimeout = 60 * time.Second
	// RequestBurst and RequestRefill bound how fast one connection may ask.
	RequestBurst           = 20
	RequestRefillPerSecond = 10
)

// AuthMode decides what an unauthenticated connection may do (FR-112).
type AuthMode string

const (
	// AuthOptional lets an unauthenticated client read and write content.
	AuthOptional AuthMode = "optional"
	// AuthRequired confines an unauthenticated client to status and login.
	AuthRequired AuthMode = "required"
)

// ParseAuthMode validates the --control-auth value.
func ParseAuthMode(raw string) (AuthMode, error) {
	switch AuthMode(raw) {
	case AuthOptional:
		return AuthOptional, nil
	case AuthRequired:
		return AuthRequired, nil
	default:
		return "", fmt.Errorf("unknown control auth mode %q (want %q or %q)", raw, AuthOptional, AuthRequired)
	}
}

// capabilityVocabulary is FR-122b: the service must not advertise anything else.
var capabilityVocabulary = map[string]bool{
	"page.get":     true,
	"page.apply":   true,
	"auth.login":   true,
	"vectors.wait": true,
}

// Source supplies the wiki facts the channel cannot know itself.
type Source interface {
	// ControlStatus reports the current state, without side effects.
	ControlStatus(ctx context.Context) SourceStatus
	// ControlUser re-reads one account, so a disabled or deleted user stops
	// working on its next request (FR-117). A user that is gone comes back as
	// ErrNoSuchUser; any other error is transient and must not end the session.
	ControlUser(ctx context.Context, userID string) (SourceUser, error)
	// ControlVerifyCredentials checks a credential pair without issuing a
	// session (FR-111).
	ControlVerifyCredentials(ctx context.Context, identifier, password string) (SourceUser, error)
}

// PeerUID reports the uid on the other end of a connection. The client checks it
// too: a socket someone else listens on is not the wiki (FR-103b).
func PeerUID(conn net.Conn) (uint32, bool) { return peerUID(conn) }

// OwnerUID reports who owns path, so a client can tell whose descriptor it read.
func OwnerUID(path string) (uint32, bool) { return ownerUID(path) }

// ErrNoSuchUser says the account is gone, which is the one case that ends an
// authenticated session. Every other error is transient (FR-117).
var ErrNoSuchUser = errors.New("control: the account does not exist")

// SystemUserID is the identity an anonymous session writes as: the same trust
// level as an external file edit, attributed to the system user (FR-113, C-8).
const SystemUserID = "system"

// SourceUser is the identity behind a verified credential.
type SourceUser struct {
	ID       string
	Username string
	Role     string
}

// SourceStatus is what the composition root knows about the wiki.
type SourceStatus struct {
	AuthDisabled bool
	Vectors      VectorsStatus
	Pages        int
	Indexing     IndexingStatus
}

// VectorsStatus reports the semantic index: mode, queue depth, failures, and
// whether the encoder loaded (FR-122).
type VectorsStatus struct {
	Mode             string `json:"mode"`
	Paused           bool   `json:"paused"`
	Pending          int    `json:"pending"`
	Failed           int    `json:"failed"`
	EncoderAvailable bool   `json:"encoder_available"`
}

// IndexingStatus reports the keyword-index bootstrap.
type IndexingStatus struct {
	Running bool `json:"running"`
}

// Status is what `status` answers: protocol version, capabilities, auth tier,
// vector state, page count (FR-122).
type Status struct {
	Protocol     string         `json:"protocol"`
	Capabilities []string       `json:"capabilities"`
	Auth         AuthStatus     `json:"auth"`
	Vectors      VectorsStatus  `json:"vectors"`
	Pages        int            `json:"pages"`
	Indexing     IndexingStatus `json:"indexing"`
}

// AuthStatus is the tier the channel runs in and whether auth is disabled.
type AuthStatus struct {
	Mode     string `json:"mode"`
	Disabled bool   `json:"disabled"`
}

// Descriptor is what the server publishes so a client can find the socket
// (FR-106).
type Descriptor struct {
	Socket   string `json:"socket"`
	Protocol string `json:"protocol"`
}

// Config is everything the channel needs to start.
type Config struct {
	// DataDir holds the descriptor and, by default, the socket.
	DataDir    string
	SocketPath string
	AuthMode   AuthMode
	Source     Source
	// Pages answers page.get and page.apply; nil keeps both unimplemented.
	Pages Pages
	// Capabilities are the FR-122b values this wiring implements.
	Capabilities []string
	Logger       *slog.Logger
}

// Service is the local control channel: one unix socket, JSON-RPC 2.0 frames.
type Service struct {
	dataDir        string
	socketPath     string
	descriptorPath string
	authMode       AuthMode
	source         Source
	pages          Pages
	capabilities   []string
	log            *slog.Logger

	listener net.Listener
	closing  chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	started  bool

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	stopping bool
}

// errIdle says the peer sent nothing within IdleTimeout. It is not a protocol
// error, so the connection is closed without an answer.
var errIdle = errors.New("control: the connection was idle")

// DefaultSocketPath is where the channel listens unless the operator points it
// elsewhere.
func DefaultSocketPath(dataDir string) string {
	return filepath.Join(dataDir, "control.sock")
}

// New validates the configuration and resolves the paths. It binds nothing: a
// caller that cannot start the channel must still be able to run the server
// (FR-104).
func New(cfg Config) (*Service, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("control: a data directory is required")
	}
	socketPath := cfg.SocketPath
	if socketPath == "" {
		socketPath = DefaultSocketPath(cfg.DataDir)
	}
	if !filepath.IsAbs(socketPath) {
		return nil, fmt.Errorf("control: the socket path %q must be absolute", socketPath)
	}
	mode := cfg.AuthMode
	if mode == "" {
		mode = AuthOptional
	}
	if _, err := ParseAuthMode(string(mode)); err != nil {
		return nil, err
	}
	for _, capability := range cfg.Capabilities {
		if !capabilityVocabulary[capability] {
			return nil, fmt.Errorf("control: %q is not a capability this protocol defines", capability)
		}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		dataDir:        cfg.DataDir,
		socketPath:     socketPath,
		descriptorPath: filepath.Join(cfg.DataDir, "control.json"),
		authMode:       mode,
		source:         cfg.Source,
		pages:          cfg.Pages,
		capabilities:   append([]string{}, cfg.Capabilities...),
		log:            logger,
		closing:        make(chan struct{}),
		conns:          map[net.Conn]struct{}{},
	}, nil
}

// SocketPath is where the channel listens.
func (s *Service) SocketPath() string { return s.socketPath }

// Start binds the socket, publishes the descriptor and accepts connections.
// A long socket path or an unusable one comes back as an error, and the caller
// keeps running without a channel (FR-104); it never falls back to a predictable
// temporary path.
func (s *Service) Start() error {
	listener, err := listenControl(s.socketPath)
	if err != nil {
		return err
	}
	s.listener = listener

	if err := s.checkDirectoryPermissions(); err != nil {
		s.log.Warn("control channel: the data directory is writable by others", "error", err)
	}

	if err := s.writeDescriptor(); err != nil {
		if closeErr := listener.Close(); closeErr != nil {
			s.log.Warn("control channel: could not close the listener", "error", closeErr)
		}
		// Nobody else removes this one: the service never took ownership of a
		// running channel, so leaving the socket would strand it.
		if removeErr := os.Remove(s.socketPath); removeErr != nil && !os.IsNotExist(removeErr) {
			s.log.Warn("control channel: could not remove the unused socket", "error", removeErr)
		}
		return err
	}

	s.mu.Lock()
	s.started = true
	s.mu.Unlock()

	go s.accept()
	s.log.Info("control channel listening", "socket", s.socketPath)
	return nil
}

// Stop closes the listener and every connection, then removes the descriptor and
// the socket file.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.closing)
	})
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			s.log.Warn("control channel: could not close the listener", "error", err)
		}
	}

	// Refuse new connections before draining, so a client cannot slip in behind
	// the snapshot below and outlive the wiki this channel answers for.
	s.mu.Lock()
	s.stopping = true
	ownedFiles := s.started
	s.mu.Unlock()

	s.closeConnections()
	s.wg.Wait()
	// Anything accepted in the instant between the snapshot and the flag above.
	s.closeConnections()

	if !ownedFiles {
		return
	}
	if err := os.Remove(s.descriptorPath); err != nil && !os.IsNotExist(err) {
		s.log.Warn("control channel: could not remove the descriptor", "error", err)
	}
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		s.log.Warn("control channel: could not remove the socket", "error", err)
	}
}

// listenControl binds the socket, replacing a stale one. It cannot reuse the HTTP
// helper, which does not work on Windows (FR-104b).
func listenControl(socketPath string) (net.Listener, error) {
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("control: %s exists and is not a socket", socketPath)
		}
		// What a crashed server left behind.
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("control: remove stale socket %s: %w", socketPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("control: inspect %s: %w", socketPath, err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("control: listen on %s: %w", socketPath, err)
	}
	if err := restrictSocket(socketPath); err != nil {
		if closeErr := listener.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, fmt.Errorf("control: restrict %s: %w", socketPath, err)
	}
	return listener, nil
}

// checkDirectoryPermissions warns when anyone could replace the socket (FR-105).
func (s *Service) checkDirectoryPermissions() error {
	info, err := os.Stat(s.dataDir)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if othersCanWrite(mode) {
		return fmt.Errorf("data directory %s has mode %o", s.dataDir, mode)
	}
	return nil
}

// writeDescriptor publishes where the channel listens, replacing whatever a
// crashed run left (FR-106).
func (s *Service) writeDescriptor() error {
	payload, err := json.Marshal(Descriptor{Socket: s.socketPath, Protocol: Protocol})
	if err != nil {
		return err
	}
	// Write next to the target and rename, so a client reading during a start
	// never sees a half-written descriptor. The temp file starts at 0600, which
	// also replaces a stale world-readable one (FR-106).
	temp, err := os.CreateTemp(filepath.Dir(s.descriptorPath), ".control-json-*")
	if err != nil {
		return fmt.Errorf("control: create %s: %w", s.descriptorPath, err)
	}
	tempName := temp.Name()
	defer func() {
		_ = os.Remove(tempName)
	}()
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return fmt.Errorf("control: write %s: %w", s.descriptorPath, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("control: close %s: %w", tempName, err)
	}
	if err := os.Rename(tempName, s.descriptorPath); err != nil {
		return fmt.Errorf("control: publish %s: %w", s.descriptorPath, err)
	}
	return nil
}

// accept takes connections until the service stops, refusing more than
// MaxConnections at once (FR-107).
func (s *Service) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.closing:
				return
			default:
			}
			s.log.Warn("control channel: accept failed", "error", err)
			return
		}

		if !s.addConnection(conn) {
			s.log.Warn("control channel: refusing a connection, the limit is reached", "limit", MaxConnections)
			if closeErr := conn.Close(); closeErr != nil {
				s.log.Warn("control channel: could not close a refused connection", "error", closeErr)
			}
			continue
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(conn)
		}()
	}
}

// addConnection records the connection, or refuses it when the limit is
// reached.
func (s *Service) addConnection(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || len(s.conns) >= MaxConnections {
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}

// dropConnection forgets one connection.
func (s *Service) dropConnection(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

// closeConnections closes everything that is still open, so Stop cannot hang.
func (s *Service) closeConnections() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// serve runs one connection: read a frame, dispatch it, write the answer, until
// the peer leaves or the connection goes stale (FR-107, FR-108).
func (s *Service) serve(conn net.Conn) {
	defer s.dropConnection(conn)
	defer func() {
		_ = conn.Close()
	}()

	if !s.peerAllowed(conn) {
		s.log.Warn("control channel: refusing a connection from another user")
		return
	}

	budget := &requestBudget{tokens: RequestBurst, last: time.Now()}
	stream := newFrameStream(conn)
	// One connection is one session: a successful auth.login upgrades it and
	// nothing else can (FR-111).
	session := &session{}
	for {
		select {
		case <-s.closing:
			return
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(IdleTimeout)); err != nil {
			return
		}

		request, err := stream.readRequest()
		if err != nil {
			// A quiet or closed connection gets no answer: it asked nothing.
			if errors.Is(err, io.EOF) || errors.Is(err, errIdle) {
				return
			}
			if errors.Is(err, errTooLarge) {
				s.reply(conn, errorResponse(nil, errFrameTooLarge(MaxFrameBytes)))
				return
			}
			s.reply(conn, errorResponse(nil, errParseError(err.Error())))
			return
		}

		if !budget.allow(time.Now()) {
			if err := s.reply(conn, errorResponse(request.ID, errRateLimited())); err != nil {
				return
			}
			continue
		}

		started := time.Now()
		response := s.dispatch(context.Background(), request, session)
		code := 0
		if response.Error != nil {
			code = response.Error.Code
		}
		// One line per request: method, outcome, who asked and how long it took
		// (NFR-6). Debug, because a normal client asks often.
		s.log.Debug("control request",
			"method", request.Method,
			"code", code,
			"identity", session.identity(),
			"duration_ms", time.Since(started).Milliseconds())
		if err := s.reply(conn, response); err != nil {
			return
		}
	}
}

// reply writes one response under a write deadline.
func (s *Service) reply(conn net.Conn, response *Response) error {
	if err := conn.SetWriteDeadline(time.Now().Add(IdleTimeout)); err != nil {
		return err
	}
	return writeResponse(conn, response)
}

// peerAllowed reports whether the connection comes from the user that owns the
// data directory (FR-103). Platforms without peer credentials rely on the
// socket permissions.
func (s *Service) peerAllowed(conn net.Conn) bool {
	uid, ok := peerUID(conn)
	if !ok {
		// Windows and other platforms without peer credentials rely on the
		// socket's permissions (FR-103).
		return true
	}
	owner, ownerOK := ownerUID(s.dataDir)
	if !ownerOK {
		s.log.Warn("control channel: cannot tell who owns the data directory, skipping the peer check", "data_dir", s.dataDir)
		return true
	}
	return uid == owner
}

// requestBudget is the per-connection token bucket of FR-108.
type requestBudget struct {
	tokens float64
	last   time.Time
}

// allow takes one token, refilling first.
func (b *requestBudget) allow(now time.Time) bool {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * RequestRefillPerSecond
		if b.tokens > RequestBurst {
			b.tokens = RequestBurst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
