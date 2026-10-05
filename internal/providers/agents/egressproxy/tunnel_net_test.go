//go:build integration

package egressproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDriverProxyTunnelsListedDestination(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "through the tunnel")
	}))
	defer srv.Close()
	r := startRig(context.Background(), t, DestinationAllowlist{allowed},
		map[Destination]string{allowed: srv.Listener.Addr().String()})
	proxyURL, err := url.Parse(r.p.URL())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	tr := &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12},
	}
	resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Get("https://api.example.test/")
	if err != nil {
		t.Fatalf("TLS request through the proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "through the tunnel" {
		t.Fatalf("response %d %q", resp.StatusCode, body)
	}
	tr.CloseIdleConnections()
	r.wantRows(t, row(PhaseIntent, allowed, ReasonAllowed), row(PhaseConfirm, allowed, ReasonConnected),
		row(PhaseConfirm, allowed, ReasonClosed))
	if n := r.dials.Load(); n != 1 {
		t.Fatalf("dialed %d times, want 1", n)
	}
}

func TestDriverProxyListensLoopbackOnly(t *testing.T) {
	r := startRig(context.Background(), t, nil, nil)
	host, port, err := net.SplitHostPort(r.addr(t))
	if err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatalf("URL host %q port %q (%v), want 127.0.0.1:<ephemeral>", host, port, err)
	}
	la, ok := r.p.listener.Addr().(*net.TCPAddr)
	if !ok || !la.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listener address %v, want 127.0.0.1", r.p.listener.Addr())
	}
	if !strings.HasPrefix(r.p.URL(), "http://cascade:") || !strings.HasSuffix(r.p.URL(), "@"+la.String()) {
		t.Fatalf("URL shape %q", strings.Replace(r.p.URL(), r.p.cred, "<cred>", 1))
	}
}

func TestDriverProxyCloseEndsTunnels(t *testing.T) {
	for _, viaCancel := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		r := startRig(ctx, t, DestinationAllowlist{allowed}, map[Destination]string{allowed: echoServer(t)})
		addr := r.addr(t)
		c, br, status := r.send(t, connectHead(string(allowed), basic(r.p)))
		if status != 200 {
			t.Fatalf("cancel=%t: status %d", viaCancel, status)
		}
		if viaCancel {
			cancel()
		} else if err := r.p.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := br.ReadByte(); err != io.EOF {
			t.Fatalf("cancel=%t: open tunnel read = %v, want EOF within 2s", viaCancel, err)
		}
		if nc, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = nc.Close()
			t.Fatalf("cancel=%t: the proxy port still accepts after close", viaCancel)
		}
		cancel()
	}
}

func TestProductionDialRefusesLoopbackWithoutConnecting(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	c, err := ProductionDial()(context.Background(), "tcp", l.Addr().String())
	if c != nil {
		_ = c.Close()
	}
	if dialReason(err) != ReasonResolvedAddressForbidden {
		t.Fatalf("ProductionDial to loopback: %v, want the address guard", err)
	}
	_ = l.Close()
	<-done
	if n := accepted.Load(); n != 0 {
		t.Fatalf("the guarded dial reached the listener %d times", n)
	}
}

// failingListener accepts nothing.
type failingListener struct{ net.Listener }

func (failingListener) Accept() (net.Conn, error) {
	return nil, cascade.New(cascade.KindUnavailable, "accept failed")
}

func TestStartFailurePathsCloseFailed(t *testing.T) {
	opts := Options{DriverID: DriverOpenCode, JobID: "job-2", Dial: ProductionDial(), Journal: noJournal}
	cfg := defaultSettings()
	cfg.listen = func(context.Context) (net.Listener, error) {
		return nil, cascade.New(cascade.KindUnavailable, "no port")
	}
	if p, err := start(context.Background(), opts, cfg); p != nil || !cascade.HasKind(err, cascade.KindUnavailable) ||
		!strings.Contains(err.Error(), "loopback listener could not be bound") {
		t.Fatalf("listen failure: p=%v err=%v", p, err)
	}
	cfg = defaultSettings()
	base := cfg.listen
	cfg.listen = func(ctx context.Context) (net.Listener, error) {
		l, err := base(ctx)
		return failingListener{l}, err
	}
	p, err := start(context.Background(), opts, cfg)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a proxy whose listener failed did not close itself")
	}
	_ = p.Close()
}

func TestNilConnectionFromDialIsRefused(t *testing.T) {
	r := &netRig{notify: make(chan struct{}, 1024)}
	nilDial := func(context.Context, string, string) (net.Conn, error) {
		r.dials.Add(1)
		return nil, nil
	}
	p, err := Start(context.Background(), Options{DriverID: DriverClaude, JobID: "job-7",
		Allow: DestinationAllowlist{allowed}, Dial: nilDial, Journal: r.journal})
	if err != nil {
		t.Fatal(err)
	}
	r.p = p
	t.Cleanup(func() { _ = p.Close() })
	if got := r.status(t, connectHead(string(allowed), basic(r.p))); got != 502 {
		t.Fatalf("status %d, want 502", got)
	}
	r.wantRows(t, row(PhaseIntent, allowed, ReasonAllowed), row(PhaseConfirm, allowed, ReasonDialFailed))
	if n := r.dials.Load(); n != 1 {
		t.Fatalf("dialed %d times, want 1", n)
	}
}
