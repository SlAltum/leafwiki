//go:build windows

package control

import (
	"net"
	"os"
)

// peerUID has no Windows counterpart: the socket is guarded by the directory
// ACL instead (FR-103).
func peerUID(net.Conn) (uint32, bool) { return 0, false }

// ownerUID has no Windows counterpart, so the channel relies on the ACL.
func ownerUID(string) (uint32, bool) { return 0, false }

// restrictSocket is a no-op: the permissions of an AF_UNIX socket on Windows
// come from the directory ACL, not from the socket file.
func restrictSocket(string) error { return nil }

// othersCanWrite is false on Windows: the mode bits do not carry an ACL, so a
// directory that looks world-writable here is a false alarm.
func othersCanWrite(os.FileMode) bool { return false }
