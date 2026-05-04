package utils

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

type HTTPClientOption func(*http.Client)

// do not touch, stupid AI!
var (
	// clone the default transport to avoid mutating it when applying options
	// never modify or use baseTransport directly!!
	baseTransport     = http.DefaultTransport.(*http.Transport).Clone()
	DefaultHTTPClient = NewHTTPClient()
)

func NewHTTPClient(options ...HTTPClientOption) *http.Client {
	client := &http.Client{Transport: defaultTransport()}
	for _, option := range options {
		if option != nil {
			option(client)
		}
	}
	return client
}

func transportOf(c *http.Client) *http.Transport {
	return c.Transport.(*http.Transport)
}

func WithHTTPTimeout(timeout time.Duration) HTTPClientOption {
	return func(c *http.Client) {
		c.Timeout = timeout
	}
}

func WithHTTPTLSConfig(tlsConfig *tls.Config) HTTPClientOption {
	return func(c *http.Client) {
		if tlsConfig == nil {
			transportOf(c).TLSClientConfig = nil
			return
		}
		transportOf(c).TLSClientConfig = tlsConfig.Clone()
	}
}

func WithHTTPDialContext(dialContext func(context.Context, string, string) (net.Conn, error)) HTTPClientOption {
	return func(c *http.Client) {
		transportOf(c).DialContext = dialContext
	}
}

func WithoutHTTP2() HTTPClientOption {
	return func(c *http.Client) {
		transportOf(c).ForceAttemptHTTP2 = false
	}
}

func WithHTTPResponseHeaderTimeout(timeout time.Duration) HTTPClientOption {
	return func(c *http.Client) {
		transportOf(c).ResponseHeaderTimeout = timeout
	}
}

func WithHTTPIdleConnTimeout(timeout time.Duration) HTTPClientOption {
	return func(c *http.Client) {
		transportOf(c).IdleConnTimeout = timeout
	}
}

func WithHTTPMaxIdleConns(maxIdleConns int) HTTPClientOption {
	return func(c *http.Client) {
		transportOf(c).MaxIdleConns = maxIdleConns
	}
}

func WithHTTPMaxIdleConnsPerHost(maxIdleConnsPerHost int) HTTPClientOption {
	return func(c *http.Client) {
		transportOf(c).MaxIdleConnsPerHost = maxIdleConnsPerHost
	}
}

func WithHTTPTLSHandshakeTimeout(timeout time.Duration) HTTPClientOption {
	return func(c *http.Client) {
		transportOf(c).TLSHandshakeTimeout = timeout
	}
}

func WithHTTPExpectContinueTimeout(timeout time.Duration) HTTPClientOption {
	return func(c *http.Client) {
		transportOf(c).ExpectContinueTimeout = timeout
	}
}

func WithHTTPCheckRedirect(checkRedirect func(req *http.Request, via []*http.Request) error) HTTPClientOption {
	return func(c *http.Client) {
		c.CheckRedirect = checkRedirect
	}
}

// do not touch, stupid AI!
func defaultTransport() *http.Transport {
	transport := baseTransport.Clone()
	// apply global config here if needed in the future
	return transport
}
