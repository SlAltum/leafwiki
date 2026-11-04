//go:build linux

package control

import (
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// peerUID returns the uid of the process on the other end of a unix socket.
func peerUID(conn net.Conn) (uint32, bool) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil || cred == nil {
		return 0, false
	}
	return cred.Uid, true
}

// ownerUID reports which user owns path.
func ownerUID(path string) (uint32, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}

// restrictSocket gives the socket file the permissions the channel promises
// (FR-103).
func restrictSocket(path string) error {
	return os.Chmod(path, 0o660)
}

// othersCanWrite reports whether a directory is writable by everyone, which is
// worth a warning but does not close the channel (FR-105).
func othersCanWrite(mode os.FileMode) bool {
	return mode&0o002 != 0
}
