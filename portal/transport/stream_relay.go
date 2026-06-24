package transport

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

const defaultSessionWriteLimit = 5 * time.Second

var (
	keepaliveMarker = []byte{types.MarkerKeepalive}
	rawStartMarker  = []byte{types.MarkerRawStart}
	tlsStartMarker  = []byte{types.MarkerTLSStart}
)

var errStreamFull = errors.New("stream ready queue full")

type RelayStream struct {
	notify       chan struct{}
	identityKey  string
	ready        list.List
	idleInterval time.Duration
	readyLimit   int
	closedErr    error
	mu           sync.Mutex
}

func NewRelayStream(identityKey string, idleInterval time.Duration, readyLimit int) *RelayStream {
	return &RelayStream{
		identityKey:  identityKey,
		idleInterval: idleInterval,
		readyLimit:   readyLimit,
		notify:       make(chan struct{}, 1),
	}
}

func (b *RelayStream) OfferConn(conn net.Conn) error {
	if conn == nil {
		return errors.New("reverse connection is required")
	}
	conn = utils.NewQuickACKConn(conn)
	session := newRelaySession(conn, b.idleInterval)

	b.mu.Lock()
	if b.closedErr != nil {
		err := b.closedErr
		b.mu.Unlock()
		_ = session.Close()
		return err
	}

	if b.readyLimit > 0 && b.ready.Len() >= b.readyLimit {
		b.mu.Unlock()
		_ = session.Close()
		return errStreamFull
	}

	session.elem = b.ready.PushBack(session)
	session.StartIdle()
	b.signalLocked()
	b.mu.Unlock()

	go b.watchSession(session)
	return nil
}

func (b *RelayStream) Claim(ctx context.Context) (net.Conn, error) {
	return b.claimWithMarker(ctx, types.MarkerTLSStart)
}

func (b *RelayStream) claimRaw(ctx context.Context) (net.Conn, error) {
	return b.claimWithMarker(ctx, types.MarkerRawStart)
}

func (b *RelayStream) claimWithMarker(ctx context.Context, marker byte) (net.Conn, error) {
	for {
		b.mu.Lock()
		for b.closedErr == nil && b.ready.Len() == 0 {
			b.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-b.notify:
			}
			b.mu.Lock()
		}
		if b.closedErr != nil {
			err := b.closedErr
			b.mu.Unlock()
			return nil, err
		}

		elem := b.ready.Front()
		session := elem.Value.(*relaySession)
		b.ready.Remove(elem)
		session.elem = nil
		b.mu.Unlock()

		if session.IsClosed() {
			continue
		}
		if err := session.activateWithMarker(marker); err != nil {
			_ = session.Close()
			continue
		}
		return session, nil
	}
}

func (b *RelayStream) Close() {
	b.mu.Lock()
	var sessions []*relaySession
	for elem := b.ready.Front(); elem != nil; elem = elem.Next() {
		sessions = append(sessions, elem.Value.(*relaySession))
	}
	b.ready.Init()
	if b.closedErr == nil {
		b.closedErr = net.ErrClosed
	}
	b.signalLocked()
	b.mu.Unlock()

	for _, session := range sessions {
		_ = session.Close()
	}
}

func (b *RelayStream) ReadyCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ready.Len()
}

func (b *RelayStream) watchSession(session *relaySession) {
	<-session.Done()

	b.mu.Lock()
	if session.elem != nil {
		b.ready.Remove(session.elem)
		session.elem = nil
	}
	readyCount := b.ready.Len()
	b.mu.Unlock()

	log.Info().
		Str("identity_key", b.identityKey).
		Str("remote_addr", session.remoteAddrString()).
		Int("ready", readyCount).
		Msg("sdk reverse disconnected")
}

func (b *RelayStream) signalLocked() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

type sessionState int

const (
	sessionIdle sessionState = iota
	sessionClaimed
	sessionClosed
)

type relaySession struct {
	conn          net.Conn
	keepaliveStop chan struct{}
	keepaliveDone chan struct{}
	done          chan struct{}
	elem          *list.Element
	idleInterval  time.Duration
	state         atomic.Int32
	closeOnce     sync.Once
	mu            sync.Mutex
}

func newRelaySession(conn net.Conn, idleInterval time.Duration) *relaySession {
	s := &relaySession{
		conn:         conn,
		idleInterval: idleInterval,
		done:         make(chan struct{}),
	}
	s.state.Store(int32(sessionIdle))
	return s
}

func (s *relaySession) Read(p []byte) (int, error) {
	return s.conn.Read(p)
}

func (s *relaySession) Write(p []byte) (int, error) {
	return s.conn.Write(p)
}

func (s *relaySession) LocalAddr() net.Addr {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.LocalAddr()
}

func (s *relaySession) RemoteAddr() net.Addr {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.RemoteAddr()
}

func (s *relaySession) SetDeadline(t time.Time) error {
	return s.conn.SetDeadline(t)
}

func (s *relaySession) SetReadDeadline(t time.Time) error {
	return s.conn.SetReadDeadline(t)
}

func (s *relaySession) SetWriteDeadline(t time.Time) error {
	return s.conn.SetWriteDeadline(t)
}

func (s *relaySession) Done() <-chan struct{} {
	return s.done
}

func (s *relaySession) Unwrap() net.Conn {
	return s.conn
}

func (s *relaySession) remoteAddrString() string {
	if s == nil || s.conn == nil || s.conn.RemoteAddr() == nil {
		return ""
	}
	return s.conn.RemoteAddr().String()
}

func (s *relaySession) IsClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *relaySession) StartIdle() {
	s.mu.Lock()
	if s.state.Load() != int32(sessionIdle) || s.keepaliveStop != nil {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	s.keepaliveStop = stop
	s.keepaliveDone = done
	s.mu.Unlock()

	go s.runKeepalive(stop, done)
}

func (s *relaySession) Activate() error {
	return s.activateWithMarker(types.MarkerTLSStart)
}

func (s *relaySession) activateWithMarker(marker byte) error {
	if !s.state.CompareAndSwap(int32(sessionIdle), int32(sessionClaimed)) {
		state := s.state.Load()
		return fmt.Errorf("session not idle: %d", state)
	}

	s.mu.Lock()
	stop := s.keepaliveStop
	done := s.keepaliveDone
	s.keepaliveStop = nil
	s.keepaliveDone = nil
	s.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Load() == int32(sessionClosed) {
		return net.ErrClosed
	}
	var markerBuf []byte
	switch marker {
	case types.MarkerKeepalive:
		markerBuf = keepaliveMarker
	case types.MarkerRawStart:
		markerBuf = rawStartMarker
	case types.MarkerTLSStart:
		markerBuf = tlsStartMarker
	default:
		markerBuf = []byte{marker}
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(defaultSessionWriteLimit))
	_, err := s.conn.Write(markerBuf)
	_ = s.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		_ = s.Close()
	}
	return err
}

func (s *relaySession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.state.Store(int32(sessionClosed))

		s.mu.Lock()
		stop := s.keepaliveStop
		done := s.keepaliveDone
		s.keepaliveStop = nil
		s.keepaliveDone = nil
		conn := s.conn
		s.mu.Unlock()

		if stop != nil {
			close(stop)
		}
		if done != nil {
			<-done
		}

		err = conn.Close()
		close(s.done)
	})
	return err
}

func (s *relaySession) runKeepalive(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(s.idleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-s.done:
			return
		case <-ticker.C:
		}

		s.mu.Lock()
		if s.state.Load() != int32(sessionIdle) {
			s.mu.Unlock()
			return
		}
		_ = s.conn.SetWriteDeadline(time.Now().Add(defaultSessionWriteLimit))
		_, err := s.conn.Write(keepaliveMarker)
		_ = s.conn.SetWriteDeadline(time.Time{})
		s.mu.Unlock()
		if err != nil {
			_ = s.Close()
			return
		}
	}
}
