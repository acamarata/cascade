package client

import (
	"net/http"
	"time"
)

// NewReleaseTransport returns an http.Transport configured for downloading
// release assets. It disables proxies, forces HTTP/2 attempts, and applies
// timeouts tuned for release downloads: 15s for TLS handshake, 60s for
// response headers, 30s idle timeout, and a maximum of 4 idle connections.
func NewReleaseTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          4,
	}
}
