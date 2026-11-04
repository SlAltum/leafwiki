//go:build !linux && !darwin && !windows

package control

import (
	"net"
	"os"
)

// peerUID is unavailable on this platform; the channel relies on the socket
// permissions alone.
func peerUID(net.Conn) (uint32, bool) { return 0, false }

// ownerUID is unavailable on this platform.
func ownerUID(string) (uint32, bool) { return 0, false }

// restrictSocket gives the socket file the permissions the channel promises
// (FR-103).
func restrictSocket(path string) error { return os.Chmod(path, 0o660) }

// othersCanWrite reports whether a directory is writable by everyone.
func othersCanWrite(mode os.FileMode) bool {
	return mode&0o002 != 0
}
