package egressproxy

// Purpose: the tunnel cap and the byte copy behind an admitted CONNECT.
//
// Constraints: tunneled bytes are copied, never inspected, logged or
// journaled. Either direction ending closes both sides. A confirm/connected
// row that cannot be journaled refuses the tunnel before the 200 is sent.

import (
	"context"
	"io"
	"sync"
)

// reserve claims one of the cap's tunnel slots.
func (p *Proxy) reserve() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tunnels >= p.cfg.maxTunnels {
		return false
	}
	p.tunnels++
	return true
}

// release returns a slot claimed by reserve.
func (p *Proxy) release() {
	p.mu.Lock()
	p.tunnels--
	p.mu.Unlock()
}

// tunnel confirms the connection, answers 200, forwards any bytes the
// client sent early, and copies both ways until either side ends.
func (p *Proxy) tunnel(conn, up io.ReadWriteCloser, pending []byte, dest Destination) {
	if !p.track(up) {
		_ = up.Close()
		p.refuse(conn, p.row(PhaseConfirm, dest, ReasonDialFailed))
		return
	}
	defer p.untrack(up)
	defer func() { _ = up.Close() }()
	if err := p.record(p.ctx, p.row(PhaseConfirm, dest, ReasonConnected)); err != nil {
		p.refuse(conn, p.row(PhaseDecide, dest, ReasonJournalUnavailable))
		return
	}
	defer func() { _ = p.record(context.WithoutCancel(p.ctx), p.row(PhaseConfirm, dest, ReasonClosed)) }()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if len(pending) > 0 {
		if _, err := up.Write(pending); err != nil {
			return
		}
	}
	splice(conn, up)
}

// splice copies both directions and closes both sides as soon as either
// direction ends, then waits for the other copy to return.
func splice(a, b io.ReadWriteCloser) {
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); defer stop(); _, _ = io.Copy(b, a) }()
	go func() { defer wg.Done(); defer stop(); _, _ = io.Copy(a, b) }()
	wg.Wait()
}
