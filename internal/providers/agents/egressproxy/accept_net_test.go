//go:build integration

package egressproxy

import (
	"context"
	"net"
	"testing"
	"time"
)

// gatedListener hands out one in-memory connection when released, then
// fails every Accept.
type gatedListener struct {
	net.Listener
	release chan struct{}
	handed  chan net.Conn
	served  bool
}

func (g *gatedListener) Accept() (net.Conn, error) {
	if g.served {
		return nil, net.ErrClosed
	}
	g.served = true
	<-g.release
	a, b := net.Pipe()
	g.handed <- b
	return a, nil
}

func TestAcceptAfterCloseDropsTheConnection(t *testing.T) {
	g := &gatedListener{release: make(chan struct{}), handed: make(chan net.Conn, 1)}
	cfg := defaultSettings()
	base := cfg.listen
	cfg.listen = func(ctx context.Context) (net.Listener, error) {
		l, err := base(ctx)
		g.Listener = l
		return g, err
	}
	opts := Options{DriverID: DriverAntigravity, JobID: "job-3", Dial: ProductionDial(), Journal: noJournal}
	p, err := start(context.Background(), opts, cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.closed = true // the race: Close has begun when Accept returns
	p.mu.Unlock()
	close(g.release)
	peer := <-g.handed
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("a connection accepted after close was served")
	}
	_ = p.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.conns) != 0 {
		t.Fatalf("%d connections still tracked after close", len(p.conns))
	}
}

func TestDialErrorThroughAdaptDialIs502(t *testing.T) {
	r := startRig(context.Background(), t, DestinationAllowlist{allowed}, map[Destination]string{})
	if got := r.status(t, connectHead(string(allowed), basic(r.p))); got != 502 {
		t.Fatalf("status %d, want 502", got)
	}
	r.wantRows(t, row(PhaseIntent, allowed, ReasonAllowed), row(PhaseConfirm, allowed, ReasonDialFailed))
	if n := r.dials.Load(); n != 1 {
		t.Fatalf("dialed %d times, want 1", n)
	}
}
