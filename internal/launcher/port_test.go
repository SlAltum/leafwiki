package launcher

import (
	"errors"
	"net"
	"testing"
)

func TestListen_RandomPortAndPinnedPort(t *testing.T) {
	listener, port, err := Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() {
		_ = listener.Close()
	}()

	if port == 0 {
		t.Fatal("Listen() returned port 0")
	}
	if got := listener.Addr().(*net.TCPAddr).Port; got != port {
		t.Fatalf("reported port %d, listener says %d", port, got)
	}
}

func TestListen_PinnedPortInUseIsAPortError(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer func() {
		_ = busy.Close()
	}()

	port := busy.Addr().(*net.TCPAddr).Port
	_, _, err = Listen("127.0.0.1", port)
	if err == nil {
		t.Fatal("Listen() error = nil, want a bind failure")
	}

	var portErr *PortError
	if !errors.As(err, &portErr) {
		t.Fatalf("Listen() error = %T, want *PortError", err)
	}
	if portErr.Port != port {
		t.Fatalf("PortError.Port = %d, want %d", portErr.Port, port)
	}
}

func TestRepick_ReturnsAnUnusedPort(t *testing.T) {
	listener, port, err := Repick("127.0.0.1")
	if err != nil {
		t.Fatalf("Repick() error = %v", err)
	}
	defer func() {
		_ = listener.Close()
	}()
	if port == 0 {
		t.Fatal("Repick() returned port 0")
	}
}
