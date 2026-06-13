package keyless

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	keylesstls "github.com/gosuda/keyless_tls/keyless"

	"github.com/gosuda/portal-tunnel/v2/utils"
)

func BuildClientTLSConfig(relayURL, hostname string, echKeys []tls.EncryptedClientHelloKey, headers func() http.Header, pqc bool) (*tls.Config, ioCloser, error) {
	normalizedRelayURL, err := utils.NormalizeRelayURL(relayURL)
	if err != nil {
		return nil, nil, err
	}

	parsed, err := url.Parse(normalizedRelayURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse relay url: %w", err)
	}
	serverName := parsed.Hostname()
	if serverName == "" {
		return nil, nil, errors.New("relay hostname is required")
	}

	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return nil, nil, errors.New("keyless hostname is required")
	}

	cacheKey := normalizedRelayURL + "|" + serverName + "|" + strconv.FormatBool(pqc)

	signerCacheMu.Lock()
	entry := signerCache[cacheKey]
	if entry != nil {
		entry.addRef()
	}
	signerCacheMu.Unlock()

	if entry != nil {
		if verifyErr := VerifyCertificateHostname(entry.certPEM, hostname); verifyErr == nil {
			tlsConfig := entry.tlsConfig.Clone()
			tlsConfig.CurvePreferences = utils.CurvePreferences(pqc)
			return tlsConfig, &cachedCloser{entry: entry, key: cacheKey}, nil
		}
		entry.release(cacheKey)
	}

	certPEM, rootCAPEM, err := ResolveMaterials(context.Background(), normalizedRelayURL, serverName, pqc)
	if err != nil {
		return nil, nil, fmt.Errorf("prepare keyless materials: %w", err)
	}
	if verifyErr := VerifyCertificateHostname(certPEM, hostname); verifyErr != nil {
		return nil, nil, fmt.Errorf("keyless certificate does not cover %s: %w", hostname, verifyErr)
	}

	remoteSigner, err := keylesstls.NewRemoteSigner(keylesstls.RemoteSignerConfig{
		Endpoint:   normalizedRelayURL,
		ServerName: serverName,
		KeyID:      RelayKeyID,
		RootCAPEM:  rootCAPEM,
		Headers:    headers,
	}, certPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("create keyless remote signer: %w", err)
	}

	tlsConfig, err := keylesstls.NewServerTLSConfig(keylesstls.ServerTLSConfig{
		CertPEM:                  certPEM,
		Signer:                   remoteSigner,
		NextProtos:               []string{"http/1.1"},
		MinVersion:               MinTLSVersion(len(echKeys) > 0),
		EncryptedClientHelloKeys: echKeys,
	})
	if err != nil {
		_ = remoteSigner.Close()
		return nil, nil, fmt.Errorf("create keyless tls config: %w", err)
	}
	tlsConfig.CurvePreferences = utils.CurvePreferences(pqc)

	entry = &signerCacheEntry{
		tlsConfig: tlsConfig,
		signer:    remoteSigner,
		certPEM:   certPEM,
	}
	entry.addRef()

	signerCacheMu.Lock()
	old := signerCache[cacheKey]
	signerCache[cacheKey] = entry
	signerCacheMu.Unlock()
	if old != nil {
		old.release(cacheKey)
	}

	return tlsConfig.Clone(), &cachedCloser{entry: entry, key: cacheKey}, nil
}

type ioCloser interface {
	Close() error
}

var (
	signerCacheMu sync.Mutex
	signerCache   = make(map[string]*signerCacheEntry)
)

type signerCacheEntry struct {
	tlsConfig *tls.Config
	signer    ioCloser
	certPEM   []byte
	refs      int64
	mu        sync.Mutex
}

func (e *signerCacheEntry) addRef() {
	e.mu.Lock()
	e.refs++
	e.mu.Unlock()
}

func (e *signerCacheEntry) release(key string) {
	e.mu.Lock()
	e.refs--
	shouldClose := e.refs <= 0
	e.mu.Unlock()
	if shouldClose {
		_ = e.signer.Close()
		signerCacheMu.Lock()
		if signerCache[key] == e {
			delete(signerCache, key)
		}
		signerCacheMu.Unlock()
	}
}

type cachedCloser struct {
	entry *signerCacheEntry
	key   string
	once  sync.Once
}

func (c *cachedCloser) Close() error {
	c.once.Do(func() { c.entry.release(c.key) })
	return nil
}

func ResolveMaterials(ctx context.Context, endpoint, serverName string, pqc bool) ([]byte, []byte, error) {
	chainPEM, err := utils.FetchEndpointCertificateChain(ctx, endpoint, serverName, pqc)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch signer certificate chain: %w", err)
	}
	if len(chainPEM) == 0 {
		return nil, nil, errors.New("keyless certificate chain is required")
	}
	return bytes.Clone(chainPEM), bytes.Clone(chainPEM), nil
}

func VerifyCertificateHostname(certPEM []byte, hostname string) error {
	leaf, err := utils.ParseCertificatePEM(certPEM)
	if err != nil {
		return err
	}
	return leaf.VerifyHostname(hostname)
}
