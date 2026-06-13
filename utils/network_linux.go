//go:build linux

package utils

import (
	"net"
	"syscall"
)

// SetTCPQuickACK enables TCP_QUICKACK on Linux to disable Nagle's delayed ACKs.
func SetTCPQuickACK(conn net.Conn) {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		rawConn, err := tcpConn.SyscallConn()
		if err == nil {
			_ = rawConn.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_QUICKACK, 1)
			})
		}
	}
}
