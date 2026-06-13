//go:build !linux

package utils

import "net"

// SetTCPQuickACK is a no-op on non-Linux platforms.
func SetTCPQuickACK(conn net.Conn) {}
