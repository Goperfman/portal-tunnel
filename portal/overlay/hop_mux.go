package overlay

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
)

const (
	hopProtocolVersion    = 1
	hopPrefaceLimit       = 4 << 10
	hopRegistryFrameLimit = 8 << 20
	hopIncomingBuffer     = 128
	defaultPrefaceTimeout = 2 * time.Second
)

const (
	hopModeTLSStream = "tls-stream"
	hopModeRegistry  = "registry"
)

type HopMux struct {
	listener net.Listener
	overlay  *Overlay
	incoming chan HopStream
	registry chan HopRegistryStream

	mu       sync.Mutex
	outbound map[string]*yamux.Session
	done     chan struct{}
}

type hopPreface struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
	Token   string `json:"token"`
}

type HopStream struct {
	Conn  net.Conn
	Token string
}

type HopRegistryStream struct {
	Path string
	Body []byte
	conn net.Conn
}

type hopRegistryHTTPResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

type hopRegistryRequest struct {
	Path string `json:"path"`
	Body []byte `json:"body"`
}

type hopRegistryResponse struct {
	Status int    `json:"status"`
	Body   []byte `json:"body"`
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
		incoming: make(chan HopStream, hopIncomingBuffer),
		registry: make(chan HopRegistryStream, hopIncomingBuffer),
		outbound: make(map[string]*yamux.Session),
		done:     make(chan struct{}),
	}, nil
}

func (m *HopMux) Serve(ctx context.Context) error {
	if m == nil || m.listener == nil {
		<-ctx.Done()
		return nil
	}
	go func() {
		<-ctx.Done()
		_ = m.Close()
	}()

	for {
		conn, err := m.listener.Accept()
		switch {
		case err == nil:
			go m.serveSession(ctx, conn)
		case errors.Is(err, net.ErrClosed):
			return nil
		default:
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept hop mux connection: %w", err)
		}
	}
}

func (m *HopMux) Accept(ctx context.Context) (HopStream, error) {
	if m == nil {
		<-ctx.Done()
		return HopStream{}, ctx.Err()
	}
	select {
	case stream := <-m.incoming:
		return stream, nil
	case <-ctx.Done():
		return HopStream{}, ctx.Err()
	}
}

func (m *HopMux) AcceptRegistry(ctx context.Context) (HopRegistryStream, error) {
	if m == nil {
		<-ctx.Done()
		return HopRegistryStream{}, ctx.Err()
	}
	select {
	case stream := <-m.registry:
		return stream, nil
	case <-ctx.Done():
		return HopRegistryStream{}, ctx.Err()
	}
}

func (m *HopMux) Close() error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	if m.done == nil {
		m.done = make(chan struct{})
	}
	select {
	case <-m.done:
		m.mu.Unlock()
		return nil
	default:
	}
	close(m.done)
	sessions := make([]*yamux.Session, 0, len(m.outbound))
	for _, session := range m.outbound {
		if session == nil {
			continue
		}
		sessions = append(sessions, session)
	}
	m.outbound = make(map[string]*yamux.Session)
	listener := m.listener
	m.mu.Unlock()

	var closeErr error
	if listener != nil {
		closeErr = errors.Join(closeErr, listener.Close())
	}
	for _, session := range sessions {
		closeErr = errors.Join(closeErr, session.Close())
	}
	return closeErr
}

func (m *HopMux) OpenStream(ctx context.Context, overlayIPv4, token string) (net.Conn, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("next hop token is required")
	}

	stream, err := m.openYamuxStream(ctx, overlayIPv4)
	if err != nil {
		return nil, err
	}
	preface := hopPreface{
		Version: hopProtocolVersion,
		Mode:    hopModeTLSStream,
		Token:   token,
	}
	if err := writeFramedJSON(stream, preface, hopPrefaceLimit); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}

func (m *HopMux) OpenRegistry(ctx context.Context, overlayIPv4, path string, payload any) (int, []byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, nil, errors.New("registry path is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}

	stream, err := m.openYamuxStream(ctx, overlayIPv4)
	if err != nil {
		return 0, nil, err
	}
	defer stream.Close()

	preface := hopPreface{
		Version: hopProtocolVersion,
		Mode:    hopModeRegistry,
	}
	if err := writeFramedJSON(stream, preface, hopPrefaceLimit); err != nil {
		return 0, nil, err
	}
	if err := writeFramedJSON(stream, hopRegistryRequest{Path: path, Body: body}, hopRegistryFrameLimit); err != nil {
		return 0, nil, err
	}

	var resp hopRegistryResponse
	if err := readFramedJSON(stream, &resp, hopRegistryFrameLimit); err != nil {
		return 0, nil, err
	}
	return resp.Status, resp.Body, nil
}

func (m *HopMux) openYamuxStream(ctx context.Context, overlayIPv4 string) (*yamux.Stream, error) {
	overlayIPv4 = strings.TrimSpace(overlayIPv4)
	if overlayIPv4 == "" {
		return nil, errors.New("next hop overlay ipv4 is required")
	}

	session, err := m.session(ctx, overlayIPv4)
	if err != nil {
		return nil, err
	}
	stream, err := session.OpenStream()
	if err != nil {
		m.forgetSession(overlayIPv4, session)
		_ = session.Close()
		return nil, err
	}
	return stream, nil
}

func (m *HopMux) session(ctx context.Context, overlayIPv4 string) (*yamux.Session, error) {
	m.mu.Lock()
	select {
	case <-m.done:
		m.mu.Unlock()
		return nil, net.ErrClosed
	default:
	}
	if session := m.outbound[overlayIPv4]; session != nil {
		if !session.IsClosed() {
			m.mu.Unlock()
			return session, nil
		}
	}
	delete(m.outbound, overlayIPv4)
	m.mu.Unlock()

	if m.overlay == nil || m.overlay.stack == nil {
		return nil, errors.New("overlay is not initialized")
	}
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
	select {
	case <-m.done:
		m.mu.Unlock()
		_ = session.Close()
		return nil, net.ErrClosed
	default:
	}
	if current := m.outbound[overlayIPv4]; current != nil && !current.IsClosed() {
		m.mu.Unlock()
		_ = session.Close()
		return current, nil
	}
	m.outbound[overlayIPv4] = session
	m.mu.Unlock()
	return session, nil
}

func (m *HopMux) forgetSession(overlayIPv4 string, session *yamux.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.outbound[overlayIPv4] == session {
		delete(m.outbound, overlayIPv4)
	}
}

func (m *HopMux) serveSession(ctx context.Context, conn net.Conn) {
	session, err := yamux.Server(conn, hopYamuxConfig())
	if err != nil {
		_ = conn.Close()
		log.Warn().Err(err).Msg("create hop yamux session")
		return
	}
	m.mu.Lock()
	select {
	case <-m.done:
		m.mu.Unlock()
		_ = session.Close()
		return
	default:
	}
	done := m.done
	m.mu.Unlock()
	sessionDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-done:
			_ = session.Close()
		case <-sessionDone:
		}
	}()
	defer func() {
		close(sessionDone)
		_ = session.Close()
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
	_ = stream.SetReadDeadline(time.Now().Add(defaultPrefaceTimeout))
	var preface hopPreface
	err := readFramedJSON(stream, &preface, hopPrefaceLimit)
	_ = stream.SetReadDeadline(time.Time{})
	if err != nil {
		_ = stream.Close()
		return
	}
	if preface.Version != hopProtocolVersion {
		_ = stream.Close()
		return
	}
	switch preface.Mode {
	case hopModeTLSStream:
		if strings.TrimSpace(preface.Token) == "" {
			_ = stream.Close()
			return
		}
		m.deliver(ctx, HopStream{
			Conn:  stream,
			Token: preface.Token,
		})
	case hopModeRegistry:
		var req hopRegistryRequest
		if err := readFramedJSON(stream, &req, hopRegistryFrameLimit); err != nil {
			_ = stream.Close()
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			_ = stream.Close()
			return
		}
		m.deliverRegistry(ctx, HopRegistryStream{
			Path: req.Path,
			Body: req.Body,
			conn: stream,
		})
	default:
		_ = stream.Close()
	}
}

func (m *HopMux) deliver(ctx context.Context, stream HopStream) {
	select {
	case m.incoming <- stream:
	case <-ctx.Done():
		_ = stream.Conn.Close()
	}
}

func (m *HopMux) deliverRegistry(ctx context.Context, stream HopRegistryStream) {
	select {
	case m.registry <- stream:
	case <-ctx.Done():
		_ = stream.conn.Close()
	}
}

func (s HopRegistryStream) Respond(status int, body []byte) error {
	if s.conn == nil {
		return net.ErrClosed
	}
	defer s.conn.Close()
	return writeFramedJSON(s.conn, hopRegistryResponse{Status: status, Body: body}, hopRegistryFrameLimit)
}

func (s HopRegistryStream) ServeRegistryHTTP(ctx context.Context, host string, handler http.Handler) error {
	if handler == nil {
		if s.conn != nil {
			_ = s.conn.Close()
		}
		return errors.New("hop registry handler is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+strings.TrimSpace(host)+s.Path, bytes.NewReader(s.Body))
	if err != nil {
		if s.conn != nil {
			_ = s.conn.Close()
		}
		return err
	}
	req.RemoteAddr = "127.0.0.1:0"
	req.Header.Set("Content-Type", "application/json")

	resp := &hopRegistryHTTPResponse{header: make(http.Header)}
	handler.ServeHTTP(resp, req)
	return s.Respond(resp.Status(), resp.body.Bytes())
}

func (r *hopRegistryHTTPResponse) Header() http.Header {
	return r.header
}

func (r *hopRegistryHTTPResponse) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(body)
}

func (r *hopRegistryHTTPResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *hopRegistryHTTPResponse) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func hopYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.Logger = nil
	cfg.MaxStreamWindowSize = 16 * 1024 * 1024
	cfg.StreamOpenTimeout = 75 * time.Second
	cfg.StreamCloseTimeout = 5 * time.Minute
	return cfg
}

func writeFramedJSON(w io.Writer, value any, limit int) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > limit {
		return errors.New("frame size is invalid")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(payload)))
	if err := writeAll(w, size[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

func readFramedJSON(r io.Reader, dst any, limit int) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > uint32(limit) {
		return errors.New("frame size is invalid")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, dst)
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
