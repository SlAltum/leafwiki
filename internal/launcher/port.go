package launcher

import (
	"fmt"
	"net"
	"strconv"
)

// PortError is a failed bind on a port the user pinned, so the window can name
// the port and say it is taken (SRS §6.5).
type PortError struct {
	Port int
	Err  error
}

func (e *PortError) Error() string {
	return fmt.Sprintf("listen on port %d: %v", e.Port, e.Err)
}

func (e *PortError) Unwrap() error { return e.Err }

// Listen binds the server on host, on fixed when it is non-zero and on a free
// port otherwise. It returns the listener and the port it actually got, which
// is what the window and launcher.state.json show (SRS §6.5).
func Listen(host string, fixed int) (net.Listener, int, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(fixed))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		if fixed != 0 {
			return nil, 0, &PortError{Port: fixed, Err: err}
		}
		return nil, 0, fmt.Errorf("listen on %s: %w", host, err)
	}

	port, err := listenerPort(listener)
	if err != nil {
		_ = listener.Close()
		return nil, 0, err
	}
	return listener, port, nil
}

// Repick binds a fresh random port after a restart found the remembered port
// taken (FR-108).
func Repick(host string) (net.Listener, int, error) {
	return Listen(host, 0)
}

func listenerPort(listener net.Listener) (int, error) {
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("listener %s is not a TCP address", listener.Addr())
	}
	return tcpAddr.Port, nil
}
