//go:build !linux

package utils

import "net"

// SetTCPQuickACK is a no-op on non-Linux platforms.
func SetTCPQuickACK(conn net.Conn) {}

// NewQuickACKConn is a no-op on non-Linux platforms, returning the connection as-is.
func NewQuickACKConn(conn net.Conn) net.Conn {
	return conn
}

// ConfigureListener is a no-op on non-Linux platforms.
func ConfigureListener(lc *net.ListenConfig) {}
