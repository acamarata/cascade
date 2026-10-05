package egressproxy

import (
	"bufio"
	"io"
	"testing"
)

// directTunnel runs p.tunnel on an in-memory client and returns the
// client's reader and the near end of the upstream.
func directTunnel(t *testing.T, h *harness, up io.ReadWriteCloser, pending []byte) (*pipeEnd, *bufio.Reader, chan struct{}) {
	t.Helper()
	client, server := duplex()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.p.tunnel(server, up, pending, allowed)
		_ = server.Close()
	}()
	return client, bufio.NewReader(client), done
}

func TestTunnelRefusesOnceClosed(t *testing.T) {
	h := newHarness(t, []Destination{allowed})
	h.p.mu.Lock()
	h.p.closed = true
	h.p.mu.Unlock()
	near, far := duplex()
	client, br, done := directTunnel(t, h, near, nil)
	if got := readStatus(t, br); got != 502 {
		t.Fatalf("status %d, want 502", got)
	}
	<-done
	_ = client.Close()
	if _, err := far.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("upstream read after refusal = %v, want EOF (closed)", err)
	}
	h.wantRows(t, row(PhaseConfirm, allowed, ReasonDialFailed))
}

func TestTunnelEndsOnClientWriteFailure(t *testing.T) {
	h := newHarness(t, []Destination{allowed})
	near, far := duplex()
	client, server := duplex()
	_ = client.Close() // the 200 cannot be written
	h.p.tunnel(server, near, nil, allowed)
	if _, err := far.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("upstream read = %v, want EOF", err)
	}
	h.wantRows(t, row(PhaseConfirm, allowed, ReasonConnected), row(PhaseConfirm, allowed, ReasonClosed))
}

func TestTunnelEndsOnPendingWriteFailure(t *testing.T) {
	h := newHarness(t, []Destination{allowed})
	near, far := duplex()
	_ = far.Close() // the early bytes cannot be forwarded
	client, br, done := directTunnel(t, h, near, []byte("early"))
	if got := readStatus(t, br); got != 200 {
		t.Fatalf("status %d, want 200", got)
	}
	<-done
	_ = client.Close()
	h.wantRows(t, row(PhaseConfirm, allowed, ReasonConnected), row(PhaseConfirm, allowed, ReasonClosed))
}
