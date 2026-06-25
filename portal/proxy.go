package portal

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosuda/portal-tunnel/v2/portal/policy"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

type proxy struct {
	activeConns  atomic.Int64
	tcpBytes     atomic.Int64
	tcpLoadMu    sync.Mutex
	tcpLoadAt    time.Time
	tcpLoadBytes int64
}

func (p *proxy) bridge(left, right net.Conn, identityKey string, bpsManager *policy.BPSManager) {
	p.activeConns.Add(1)
	defer p.activeConns.Add(-1)

	defer left.Close()
	defer right.Close()

	throttled := bpsManager != nil && bpsManager.IdentityBPS(identityKey) > 0

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = p.copy(right, left, identityKey, bpsManager, throttled)
		closeWrite(right)
	}()

	_ = p.copy(left, right, identityKey, bpsManager, throttled)
	closeWrite(left)
	wg.Wait()
}

func (p *proxy) activeConnectionCount() int64 {
	return p.activeConns.Load()
}

func (p *proxy) currentTCPBPS(now time.Time) float64 {
	totalTCPBytes := p.tcpBytes.Load()

	p.tcpLoadMu.Lock()
	defer p.tcpLoadMu.Unlock()

	if p.tcpLoadAt.IsZero() {
		p.tcpLoadAt = now
		p.tcpLoadBytes = totalTCPBytes
		return 0
	}

	if elapsed := now.Sub(p.tcpLoadAt); elapsed > 0 {
		tcpTrafficBPS := float64(totalTCPBytes-p.tcpLoadBytes) / elapsed.Seconds()
		p.tcpLoadAt = now
		p.tcpLoadBytes = totalTCPBytes
		return tcpTrafficBPS
	}

	return 0
}

var proxyBufPool = utils.GlobalBufferPool(64 * 1024)

func (p *proxy) copy(dst, src net.Conn, identityKey string, bpsManager *policy.BPSManager, throttled bool) error {
	buf := proxyBufPool.Get()
	defer proxyBufPool.Put(buf)

	if !throttled {
		for {
			nr, readErr := src.Read(buf)
			if nr > 0 {
				nw, writeErr := dst.Write(buf[:nr])
				if nw > 0 {
					p.tcpBytes.Add(int64(nw))
				}
				if writeErr != nil {
					return writeErr
				}
				if nw < nr {
					return io.ErrShortWrite
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return nil
				}
				return readErr
			}
		}
	}

	for {
		nr, readErr := src.Read(buf)
		if nr > 0 {
			data := buf[:nr]
			for len(data) > 0 {
				chunkSize := len(data)
				if bpsManager != nil {
					chunkSize = bpsManager.ThrottleIdentityBPS(identityKey, chunkSize)
				}

				n, err := dst.Write(data[:chunkSize])
				if n > 0 {
					p.tcpBytes.Add(int64(n))
					data = data[n:]
				}
				if err != nil {
					return err
				}
				if n == 0 {
					return io.ErrShortWrite
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func closeWrite(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := conn.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}
