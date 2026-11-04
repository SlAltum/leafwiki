package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/perber/wiki/internal/wiki/control"
)

// errChannelUnavailable means the channel could not be reached at all: the
// caller may fall back to the offline path (FR-194).
var errChannelUnavailable = errors.New("control channel unavailable")

// errChannelRefused means the channel answered badly: permission denied, a peer
// that is not us, or a hangup before the handshake. That is never a reason to
// fall back (FR-194): it may be a hijacked socket.
var errChannelRefused = errors.New("control channel refused the connection")

// controlTimeout bounds every request. A local socket answers in milliseconds;
// a slow answer means something is wrong.
const controlTimeout = 2 * time.Minute

// endpoint is where the client will connect and how it learned that.
type endpoint struct {
	socket string
	// source is "flag", "descriptor" or "default", for the diagnostics on stderr.
	source string
}

// resolveEndpoint finds the socket: the flag wins, then the descriptor, then the
// default path next to the data directory (FR-193b).
func resolveEndpoint(cfg *serverConfig) (endpoint, error) {
	if cfg.control.controlSocket != "" {
		return endpoint{socket: cfg.control.controlSocket, source: "flag"}, nil
	}
	if cfg.server.dataDir == "" {
		return endpoint{}, errors.New("--data-dir is required to find the control channel")
	}

	descriptorPath := filepath.Join(cfg.server.dataDir, "control.json")
	if payload, err := os.ReadFile(descriptorPath); err == nil {
		var descriptor control.Descriptor
		if json.Unmarshal(payload, &descriptor) == nil && descriptor.Socket != "" && filepath.IsAbs(descriptor.Socket) {
			// A descriptor someone else wrote would point us at their socket. On
			// Unix the owner has to be us (FR-193b); without owner credentials
			// (Windows) the socket must at least live in our data directory.
			owner, ok := control.OwnerUID(descriptorPath)
			if ok {
				if owner == uint32(os.Getuid()) {
					return endpoint{socket: descriptor.Socket, source: "descriptor"}, nil
				}
			} else if isInside(descriptor.Socket, cfg.server.dataDir) {
				return endpoint{socket: descriptor.Socket, source: "descriptor"}, nil
			}
		}
	}

	return endpoint{socket: control.DefaultSocketPath(cfg.server.dataDir), source: "default"}, nil
}

// isInside reports whether path lives below dir.
func isInside(path, dir string) bool {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// controlClient is one connection to the channel: one frame out, one back.
type controlClient struct {
	conn     net.Conn
	decoder  *json.Decoder
	nextID   int
	answered bool
	// lastErr is the transport failure behind the last synthetic error, so the
	// caller can classify it for the fallback chain.
	lastErr error
}

// isTimeout reports whether an IO error is a deadline.
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// dialControl connects to the socket and checks who is on the other end
// (FR-103b). A refused connection is distinct from an unreachable one, because
// only the second one may fall back (FR-194).
func dialControl(cfg *serverConfig, where endpoint) (*controlClient, error) {
	conn, err := net.DialTimeout("unix", where.socket, 5*time.Second)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOENT):
			return nil, fmt.Errorf("%w: %s", errChannelUnavailable, err)
		case errors.Is(err, syscall.EACCES), errors.Is(err, os.ErrPermission):
			return nil, fmt.Errorf("%w: %s", errChannelRefused, err)
		default:
			return nil, fmt.Errorf("%w: %s", errChannelUnavailable, err)
		}
	}

	if err := checkPeer(conn, cfg.server.dataDir); err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}

	return &controlClient{conn: conn, decoder: json.NewDecoder(conn)}, nil
}

// checkPeer refuses a socket that belongs to another user (FR-103b).
func checkPeer(conn net.Conn, dataDir string) error {
	peer, ok := control.PeerUID(conn)
	if !ok {
		// Windows and other platforms have no peer credentials; the socket's ACL
		// is the guard there (FR-103).
		return nil
	}
	if peer == uint32(os.Getuid()) {
		return nil
	}
	if owner, ownerOK := control.OwnerUID(dataDir); ownerOK && peer == owner {
		return nil
	}
	return fmt.Errorf("%w: the socket belongs to uid %d", errChannelRefused, peer)
}

// close ends the connection.
func (c *controlClient) close() {
	if c.conn != nil {
		// The request outcome is what matters; a close error is noise here.
		_ = c.conn.Close()
	}
}

// errChannelTimeout marks a request the server never answered in time, which is
// an unreachable channel (exit 6), not a refusal (FR-195).
var errChannelTimeout = errors.New("the control channel did not answer in time")

// call sends one request and returns the raw result. A JSON-RPC error comes back
// as the *control.Error it is.
func (c *controlClient) call(method string, params any) (json.RawMessage, *control.Error) {
	return c.callWithin(method, params, time.Now().Add(controlTimeout))
}

// callWithin is call with a caller-chosen deadline, so a wait loop cannot block
// past its own timeout.
func (c *controlClient) callWithin(method string, params any, deadline time.Time) (json.RawMessage, *control.Error) {
	c.nextID++
	request := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		request["params"] = params
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, &control.Error{Code: control.CodeInternal, Message: "could not set the connection deadline: " + err.Error()}
	}
	if err := json.NewEncoder(c.conn).Encode(request); err != nil {
		if isTimeout(err) {
			c.lastErr = errChannelTimeout
			return nil, &control.Error{Code: control.CodeInternal, Message: errChannelTimeout.Error() + ": " + err.Error()}
		}
		return nil, &control.Error{Code: control.CodeInternal, Message: err.Error()}
	}

	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *control.Error  `json:"error"`
	}
	if err := c.decoder.Decode(&response); err != nil {
		switch {
		case isTimeout(err):
			c.lastErr = errChannelTimeout
			return nil, &control.Error{Code: control.CodeInternal, Message: errChannelTimeout.Error() + ": " + err.Error()}
		case errors.Is(err, io.EOF) && !c.answered:
			// Closed before it ever answered: treated like a refused
			// connection, never a reason to fall back (FR-194).
			c.lastErr = errChannelRefused
			return nil, &control.Error{Code: control.CodeInternal, Message: "the channel closed before it answered"}
		case errors.Is(err, io.EOF):
			c.lastErr = errChannelUnavailable
			return nil, &control.Error{Code: control.CodeInternal, Message: "the channel closed during the request"}
		default:
			return nil, &control.Error{Code: control.CodeInternal, Message: "could not read the answer: " + err.Error()}
		}
	}
	c.answered = true
	return response.Result, response.Error
}

// credentials are what a write may present. Empty means anonymous.
type credentials struct {
	user     string
	password string
}

// login upgrades the connection (FR-193).
func (c *controlClient) login(creds credentials) *control.Error {
	_, apiErr := c.call("auth.login", map[string]any{"user": creds.user, "password": creds.password})
	return apiErr
}
