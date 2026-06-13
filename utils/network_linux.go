//go:build linux

package utils

import (
	"net"
	"sync/atomic"
	"syscall"
	"time"
)

// SetTCPQuickACK enables TCP_QUICKACK on Linux to disable Nagle's delayed ACKs.
func SetTCPQuickACK(conn net.Conn) {
	unwrapped := UnwrapConn(conn)
	if tcpConn, ok := unwrapped.(*net.TCPConn); ok {
		rawConn, err := tcpConn.SyscallConn()
		if err == nil {
			_ = rawConn.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_QUICKACK, 1)
			})
		}
	}
}

type quickACKConn struct {
	net.Conn
	tcpConn  *net.TCPConn
	lastSent atomic.Int64
}

// NewQuickACKConn wraps a connection to automatically set TCP_QUICKACK on Read/Write.
func NewQuickACKConn(conn net.Conn) net.Conn {
	if conn == nil {
		return nil
	}
	if _, ok := conn.(*quickACKConn); ok {
		return conn
	}
	unwrapped := UnwrapConn(conn)
	if tcpConn, ok := unwrapped.(*net.TCPConn); ok {
		return &quickACKConn{Conn: conn, tcpConn: tcpConn}
	}
	return conn
}

func (c *quickACKConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.enableQuickACK()
	}
	return n, err
}

func (c *quickACKConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.enableQuickACK()
	}
	return n, err
}

func (c *quickACKConn) Unwrap() net.Conn {
	return c.Conn
}

func (c *quickACKConn) enableQuickACK() {
	now := time.Now().UnixMicro()
	last := c.lastSent.Load()
	if now-last < 500 { // Limit to once per 500 microseconds (0.5ms)
		return
	}
	c.lastSent.Store(now)

	rawConn, err := c.tcpConn.SyscallConn()
	if err == nil {
		_ = rawConn.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_QUICKACK, 1)
		})
	}
}

// ConfigureListener configures the ListenConfig with Linux-specific performance options.
func ConfigureListener(lc *net.ListenConfig) {
	lc.Control = func(network, address string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			// Set TCP_DEFER_ACCEPT to defer waking up the listener until data is present.
			_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_DEFER_ACCEPT, 1)
		})
	}
}
