package client

import (
	"testing"
	"time"
)

// TestReleaseTransportSettings asserts that NewReleaseTransport returns a
// transport configured with the release-download tuning: no proxy, HTTP/2
// attempted, 15s TLS handshake, 60s response header, 30s idle timeout and
// exactly 4 idle connections.
func TestReleaseTransportSettings(t *testing.T) {
	tr := NewReleaseTransport()
	if tr.Proxy != nil {
		t.Errorf("Proxy is set, want nil")
	}
	if !tr.ForceAttemptHTTP2 {
		t.Errorf("ForceAttemptHTTP2 = false, want true")
	}
	if tr.TLSHandshakeTimeout != 15*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 15s", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != 60*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 60s", tr.ResponseHeaderTimeout)
	}
	if tr.IdleConnTimeout != 30*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 30s", tr.IdleConnTimeout)
	}
	if tr.MaxIdleConns != 4 {
		t.Errorf("MaxIdleConns = %d, want 4", tr.MaxIdleConns)
	}
}
