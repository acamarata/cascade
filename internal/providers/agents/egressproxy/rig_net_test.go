//go:build integration

package egressproxy

// Loopback rig for the integration lane: a real Start on 127.0.0.1, an
// injected Dial that maps listed destinations onto loopback test servers
// (never a real external host), and a recording Journal.

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// netRig is one started proxy plus its observers.
type netRig struct {
	p      *Proxy
	dials  atomic.Int32
	routes map[Destination]string
	mu     sync.Mutex
	rows   []Decision
	notify chan struct{}
	fail   func(Decision) error
}

// startRig starts a proxy for DriverClaude / job-7.
func startRig(ctx context.Context, t *testing.T, allow DestinationAllowlist, routes map[Destination]string) *netRig {
	t.Helper()
	r := &netRig{routes: routes, notify: make(chan struct{}, 1024)}
	p, err := Start(ctx, Options{DriverID: DriverClaude, JobID: "job-7", Allow: allow, Dial: r.dial, Journal: r.journal})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.p = p
	t.Cleanup(func() { _ = p.Close() })
	return r
}

// dial counts every attempt and maps a destination onto its loopback route.
func (r *netRig) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	r.dials.Add(1)
	target, ok := r.routes[Destination(addr)]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "rig: no route for %s", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, target)
}

// journal stores d, then applies fail.
func (r *netRig) journal(_ context.Context, d Decision) error {
	r.mu.Lock()
	r.rows = append(r.rows, d)
	r.mu.Unlock()
	r.notify <- struct{}{}
	if r.fail != nil {
		return r.fail(d)
	}
	return nil
}

// wantRows waits for len(want) rows and asserts them exactly.
func (r *netRig) wantRows(t *testing.T, want ...Decision) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		r.mu.Lock()
		got := append([]Decision(nil), r.rows...)
		r.mu.Unlock()
		if len(got) >= len(want) {
			if len(got) != len(want) {
				t.Fatalf("rows = %+v, want %+v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
				}
			}
			return
		}
		select {
		case <-r.notify:
		case <-deadline:
			t.Fatalf("timed out: rows = %+v, want %+v", got, want)
		}
	}
}

// addr is the proxy's listen address.
func (r *netRig) addr(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(r.p.URL())
	if err != nil {
		t.Fatalf("URL() %q: %v", r.p.URL(), err)
	}
	return u.Host
}

// basic is the Proxy-Authorization value for p's credential.
func basic(p *Proxy) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(p.cred))
}

// connectHead builds a CONNECT head; an empty auth omits the header.
func connectHead(target, auth string) string {
	head := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if auth != "" {
		head += "Proxy-Authorization: " + auth + "\r\n"
	}
	return head + "\r\n"
}

// send dials the proxy, writes raw and reads the response head.
func (r *netRig) send(t *testing.T, raw string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	c, err := net.DialTimeout("tcp", r.addr(t), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := io.WriteString(c, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = resp.Body.Close()
	_ = c.SetReadDeadline(time.Time{})
	return c, br, resp.StatusCode
}

// status is send plus close.
func (r *netRig) status(t *testing.T, raw string) int {
	t.Helper()
	c, _, code := r.send(t, raw)
	_ = c.Close()
	return code
}

// echoServer is a loopback TCP echo server; it returns its address.
func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return l.Addr().String()
}

// wantNoDial asserts the rig's Dial was never called.
func (r *netRig) wantNoDial(t *testing.T) {
	t.Helper()
	if n := r.dials.Load(); n != 0 {
		t.Fatalf("dialed %d times, want 0", n)
	}
}
