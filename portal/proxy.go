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
	activeConns   atomic.Int64
	tcpBytes      atomic.Int64
	tcpLoadAtNano atomic.Int64
	tcpLoadBytes  atomic.Int64
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
	nowNano := now.UnixNano()
	lastNano := p.tcpLoadAtNano.Load()
	lastBytes := p.tcpLoadBytes.Load()

	if lastNano == 0 {
		p.tcpLoadAtNano.Store(nowNano)
		p.tcpLoadBytes.Store(totalTCPBytes)
		return 0
	}

	elapsedSec := float64(nowNano-lastNano) / 1e9
	if elapsedSec <= 0 {
		return 0
	}

	bps := float64(totalTCPBytes-lastBytes) / elapsedSec
	p.tcpLoadAtNano.Store(nowNano)
	p.tcpLoadBytes.Store(totalTCPBytes)
	return bps
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
