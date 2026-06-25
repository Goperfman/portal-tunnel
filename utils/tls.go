package utils

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CurvePreferences returns the TLS 1.3 key-exchange groups to advertise.
// When pqc is true, X25519MLKEM768 is preferred for post-quantum hybrid
// protection; otherwise only classic groups are used.
func CurvePreferences(pqc bool) []tls.CurveID {
	if pqc {
		return []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256}
	}
	return []tls.CurveID{tls.X25519, tls.CurveP256}
}

func NewHTTPTLSClient(ctx context.Context, relayURL *url.URL, timeout time.Duration, pqc bool) (*tls.Config, *http.Client, *http.Transport, error) {
	cfg, client, transport, err := newHTTPTLSClient(ctx, relayURL, timeout, pqc)
	if err != nil && pqc && isHandshakeError(err) {
		return newHTTPTLSClient(ctx, relayURL, timeout, false)
	}
	return cfg, client, transport, err
}

func newHTTPTLSClient(ctx context.Context, relayURL *url.URL, timeout time.Duration, pqc bool) (*tls.Config, *http.Client, *http.Transport, error) {
	if relayURL == nil {
		return nil, nil, nil, errors.New("relay url is required")
	}

	serverName := relayURL.Hostname()
	if serverName == "" {
		return nil, nil, nil, errors.New("relay hostname is required")
	}

	var rootCAs *x509.CertPool
	if IsLocalRelayHost(serverName) {
		rootCAPEM, err := FetchEndpointCertificateChain(ctx, relayURL.String(), serverName, pqc)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("bootstrap localhost relay trust: %w", err)
		}
		rootCAs = x509.NewCertPool()
		if !rootCAs.AppendCertsFromPEM(rootCAPEM) {
			return nil, nil, nil, errors.New("failed to parse relay root ca")
		}
	}

	rawTLSConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		RootCAs:            rootCAs,
		NextProtos:         []string{"http/1.1"},
		CurvePreferences:   CurvePreferences(pqc),
		ClientSessionCache: tls.NewLRUClientSessionCache(256),
	}
	httpClient := NewHTTPClient(
		WithHTTPTLSConfig(rawTLSConfig), // will be cloned internally
		WithoutHTTP2(),
		WithHTTPTimeout(timeout),
	)
	return rawTLSConfig, httpClient, mustTransportOf(httpClient), nil
}

// FetchEndpointCertificateChain fetches the certificate chain from an endpoint.
// When pqc is true and the TLS handshake fails because the peer does not support
// the advertised post-quantum group, it retries once with classic curves only.
// This preserves discovery compatibility with portal-tunnel-original binaries.
func FetchEndpointCertificateChain(ctx context.Context, endpoint, serverName string, pqc bool) ([]byte, error) {
	chain, err := fetchEndpointCertificateChain(ctx, endpoint, serverName, pqc)
	if err != nil && pqc && isHandshakeError(err) {
		return fetchEndpointCertificateChain(ctx, endpoint, serverName, false)
	}
	return chain, err
}

func fetchEndpointCertificateChain(ctx context.Context, endpoint, serverName string, pqc bool) ([]byte, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return nil, errors.New("endpoint is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint url: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, errors.New("relay endpoint must use https")
	}

	host := u.Hostname()
	if host == "" {
		return nil, errors.New("endpoint hostname is empty")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	if serverName == "" {
		serverName = host
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("dial relay endpoint: %w", err)
	}

	tlsConn := tls.Client(rawConn, &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: IsLocalRelayHost(host),
		NextProtos:         []string{"http/1.1"},
		CurvePreferences:   CurvePreferences(pqc),
	})
	defer tlsConn.Close()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("tls handshake with relay endpoint: %w", err)
	}

	peerCerts := tlsConn.ConnectionState().PeerCertificates
	if len(peerCerts) == 0 {
		return nil, errors.New("no peer certificates from relay endpoint")
	}

	var chainPEM []byte
	for _, cert := range peerCerts {
		chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: cert.Raw,
		})...)
	}
	return chainPEM, nil
}

// isHandshakeError reports whether err is a TLS handshake failure.
func isHandshakeError(err error) bool {
	if err == nil {
		return false
	}
	var alert tls.AlertError
	if errors.As(err, &alert) {
		return true
	}
	return strings.Contains(err.Error(), "handshake")
}

func ParseCertificatePEM(pemData []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("no pem block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

func ParsePrivateKeyPEM(keyPEM []byte) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("invalid private key pem")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		switch typed := key.(type) {
		case *ecdsa.PrivateKey:
			return typed, nil
		case *rsa.PrivateKey:
			return typed, nil
		}
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("unsupported private key type")
}
