package overlay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	maxHopTokenBytes    = 256
	defaultTokenTimeout = 2 * time.Second
)

type HopMux struct {
	listener net.Listener
	overlay  *Overlay
	incoming chan HopStream

	mu       sync.Mutex
	outbound map[string]*yamux.Session
	done     chan struct{}
}

type HopStream struct {
	Conn       net.Conn
	Token      string
	RemoteAddr string
}

func NewHopMux(overlay *Overlay) (*HopMux, error) {
	if overlay == nil || overlay.stack == nil {
		return nil, errors.New("overlay is required for multi-hop mux")
	}
	listener, err := overlay.stack.ListenTCP(DefaultPeerYamuxPort)
	if err != nil {
		return nil, fmt.Errorf("listen hop yamux: %w", err)
	}
	return &HopMux{
		listener: listener,
		overlay:  overlay,
		incoming: make(chan HopStream, 1024),
		outbound: make(map[string]*yamux.Session),
		done:     make(chan struct{}),
	}, nil
}

func (m *HopMux) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = m.Close()
	}()

	for {
		conn, err := m.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("accept hop mux connection: %w", err)
		}
		go m.serveSession(ctx, conn)
	}
}

func (m *HopMux) Accept(ctx context.Context) (HopStream, error) {
	select {
	case stream := <-m.incoming:
		return stream, nil
	case <-ctx.Done():
		return HopStream{}, ctx.Err()
	}
}

func (m *HopMux) Close() error {
	m.mu.Lock()
	select {
	case <-m.done:
		m.mu.Unlock()
		return nil
	default:
	}
	close(m.done)
	sessions := m.outbound
	m.outbound = make(map[string]*yamux.Session)
	m.mu.Unlock()

	closeErr := m.listener.Close()
	for _, session := range sessions {
		closeErr = errors.Join(closeErr, session.Close())
	}
	return closeErr
}

func (m *HopMux) OpenStream(ctx context.Context, overlayIPv4, token string) (net.Conn, error) {
	if token == "" || overlayIPv4 == "" {
		return nil, errors.New("hop token and overlay ipv4 are required")
	}

	session, err := m.session(ctx, overlayIPv4)
	if err != nil {
		return nil, err
	}
	stream, err := session.OpenStream()
	if err != nil {
		m.mu.Lock()
		if m.outbound[overlayIPv4] == session {
			delete(m.outbound, overlayIPv4)
		}
		m.mu.Unlock()
		_ = session.Close()
		return nil, err
	}

	payload := []byte(token)
	if len(payload) > maxHopTokenBytes {
		_ = stream.Close()
		return nil, errors.New("next hop token is too large")
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)

	_ = stream.SetWriteDeadline(time.Now().Add(defaultTokenTimeout))
	if _, err := stream.Write(frame); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetWriteDeadline(time.Time{})
	return stream, nil
}

func (m *HopMux) session(ctx context.Context, overlayIPv4 string) (*yamux.Session, error) {
	m.mu.Lock()
	if session := m.outbound[overlayIPv4]; session != nil && !session.IsClosed() {
		m.mu.Unlock()
		return session, nil
	}
	m.mu.Unlock()

	addr := net.JoinHostPort(overlayIPv4, fmt.Sprintf("%d", DefaultPeerYamuxPort))
	conn, err := m.overlay.stack.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	session, err := yamux.Client(conn, hopYamuxConfig())
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.outbound[overlayIPv4]; current != nil && !current.IsClosed() {
		_ = session.Close()
		return current, nil
	}
	m.outbound[overlayIPv4] = session
	return session, nil
}

func (m *HopMux) serveSession(ctx context.Context, conn net.Conn) {
	session, err := yamux.Server(conn, hopYamuxConfig())
	if err != nil {
		_ = conn.Close()
		return
	}
	defer session.Close()

	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-m.done:
			_ = session.Close()
		}
	}()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return
		}
		go m.handleStream(ctx, stream)
	}
}

func (m *HopMux) handleStream(ctx context.Context, stream *yamux.Stream) {
	_ = stream.SetReadDeadline(time.Now().Add(defaultTokenTimeout))
	defer stream.SetReadDeadline(time.Time{})

	var size [4]byte
	if _, err := io.ReadFull(stream, size[:]); err != nil {
		_ = stream.Close()
		return
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > uint32(maxHopTokenBytes) {
		_ = stream.Close()
		return
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(stream, payload); err != nil {
		_ = stream.Close()
		return
	}
	token := string(payload)

	remoteAddr := ""
	if addr := stream.RemoteAddr(); addr != nil {
		remoteAddr = addr.String()
	}

	select {
	case m.incoming <- HopStream{Conn: stream, Token: token, RemoteAddr: remoteAddr}:
	case <-ctx.Done():
		_ = stream.Close()
	case <-m.done:
		_ = stream.Close()
	}
}

func hopYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.Logger = nil
	cfg.MaxStreamWindowSize = 16 * 1024 * 1024
	cfg.StreamOpenTimeout = 75 * time.Second
	cfg.StreamCloseTimeout = 5 * time.Minute
	return cfg
}
