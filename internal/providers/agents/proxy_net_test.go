//go:build integration && security

package agents

// Purpose: the spawn proxy's real-socket legs (legacy AC14): an unlisted
// destination is refused and journaled through the real audit log, a listed
// loopback server tunnels, credentials differ per spawn, and a released or
// closing proxy refuses or settles. The effect legs are in
// proxy_net_effect_test.go.
//
// Constraints: only 127.0.0.1 sockets; stored audit state is asserted.

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	netChildEnv    = "CASCADE_EFFECT_CHILD"
	netChildExit   = 3
	netAllowedDest = "api.example.test:443"
	netJob         = "job-net"
)

var netInstant = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// echo is a loopback TCP echo server; it returns its address.
func echo(t *testing.T) string {
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

// routeDial maps the listed destinations onto loopback servers and counts
// every attempt; after, when set, runs after a successful connect.
type routeDial struct {
	routes map[string]string
	mu     sync.Mutex
	dials  int
	after  func()
}

func (r *routeDial) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	r.mu.Lock()
	r.dials++
	r.mu.Unlock()
	target, ok := r.routes[addr]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "rig: no route for %s", addr)
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, network, target)
	if err == nil && r.after != nil {
		r.after()
	}
	return c, err
}

func (r *routeDial) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dials
}

// netAudit is a real audit log and effect log over an in-memory store.
func netAudit(t *testing.T, store provider.Store) (*audit.Log, *audit.EffectLog) {
	t.Helper()
	log := audit.New(store, testkit.NewFrozenClock(netInstant), nil)
	el, err := audit.NewEffectLog(log)
	if err != nil {
		t.Fatalf("NewEffectLog: %v", err)
	}
	return log, el
}

// proxyAddrAuth splits a proxy's HTTPS_PROXY into its host:port and the
// Proxy-Authorization value for its credential.
func proxyAddrAuth(t *testing.T, p *egressproxy.Proxy) (string, string) {
	t.Helper()
	u, err := url.Parse(p.URL())
	if err != nil {
		t.Fatalf("URL() %q: %v", p.URL(), err)
	}
	pw, _ := u.User.Password()
	return u.Host, "Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pw))
}

// connect sends one CONNECT and returns the status and the open connection.
func connect(t *testing.T, addr, target, auth string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	head := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if auth != "" {
		head += "Proxy-Authorization: " + auth + "\r\n"
	}
	if _, err := io.WriteString(c, head+"\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = resp.Body.Close()
	return c, br, resp.StatusCode
}

// decideEvents returns the stored policy.decide events of the log, oldest first.
func decideEvents(t *testing.T, log *audit.Log) []audit.Event {
	t.Helper()
	page, err := log.Query(context.Background(), audit.Filter{Limit: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var out []audit.Event
	for _, r := range page.Records {
		if r.Kind == audit.KindPolicyDecide {
			out = append(out, r.Event)
		}
	}
	return out
}

func TestRedTeamNetworkRefusedByBroker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	log, el := netAudit(t, storetest.NewMemStore())
	rd := &routeDial{routes: map[string]string{netAllowedDest: echo(t), "evil.example.test:443": echo(t)}}
	p, err := startSpawnProxy(ctx, egressproxy.DriverCodex, netJob, egressproxy.DestinationAllowlist{netAllowedDest},
		ProxyJournal(log, el), rd.dial)
	if err != nil {
		t.Fatalf("startSpawnProxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	addr, auth := proxyAddrAuth(t, p)

	// An unlisted destination is refused 403, journaled as a deny, never dialed.
	c, _, code := connect(t, addr, "evil.example.test:443", auth)
	_ = c.Close()
	if code != http.StatusForbidden || rd.count() != 0 {
		t.Fatalf("unlisted destination: status %d, dials %d; want 403 and no dial", code, rd.count())
	}
	evs := decideEvents(t, log)
	if len(evs) != 1 || evs[0].Verdict != "deny" || evs[0].Outcome != string(egressproxy.ReasonDestinationNotAllowed) ||
		evs[0].Actor != "agent-driver:codex" || evs[0].Action != "agent.egress.connect" ||
		!strings.Contains(string(evs[0].Explain), `"destination":"evil.example.test:443"`) {
		t.Fatalf("deny journal = %+v, want one deny of evil.example.test:443", evs)
	}

	// A credential without the proxy's password is refused before any decision about the target.
	c, _, code = connect(t, addr, netAllowedDest, "Basic "+base64.StdEncoding.EncodeToString([]byte("cascade:wrong")))
	_ = c.Close()
	if code != http.StatusProxyAuthRequired || rd.count() != 0 {
		t.Fatalf("wrong credential: status %d, dials %d; want 407 and no dial", code, rd.count())
	}

	// A listed loopback test server tunnels.
	c, br, code := connect(t, addr, netAllowedDest, auth)
	if code != http.StatusOK {
		t.Fatalf("listed destination: status %d, want 200", code)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "ping\n"); err != nil {
		t.Fatalf("tunnel write: %v", err)
	}
	if line, err := br.ReadString('\n'); err != nil || line != "ping\n" {
		t.Fatalf("tunnel echo = %q, %v; want ping", line, err)
	}
	_ = c.Close()
	if rd.count() != 1 {
		t.Fatalf("dials = %d, want exactly the one listed dial", rd.count())
	}
}

func TestSpawnProxyCredentialsDifferPerSpawn(t *testing.T) {
	ctx := context.Background()
	log, el := netAudit(t, storetest.NewMemStore())
	rd := &routeDial{routes: map[string]string{netAllowedDest: echo(t)}}
	allow := egressproxy.DestinationAllowlist{netAllowedDest}
	a, err := startSpawnProxy(ctx, egressproxy.DriverCodex, "job-a", allow, ProxyJournal(log, el), rd.dial)
	if err != nil {
		t.Fatalf("start a: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := startSpawnProxy(ctx, egressproxy.DriverCodex, "job-b", allow, ProxyJournal(log, el), rd.dial)
	if err != nil {
		t.Fatalf("start b: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	addrA, authA := proxyAddrAuth(t, a)
	_, authB := proxyAddrAuth(t, b)
	if authA == authB {
		t.Fatal("two spawns share one proxy credential")
	}
	c, _, code := connect(t, addrA, netAllowedDest, authB)
	_ = c.Close()
	if code != http.StatusProxyAuthRequired || rd.count() != 0 {
		t.Fatalf("spawn B's credential at spawn A's proxy: status %d, dials %d; want 407 and no dial", code, rd.count())
	}
}

func TestSpawnProxyClosedAtReleaseRefusesConnect(t *testing.T) {
	log, el := netAudit(t, storetest.NewMemStore())
	p, err := StartSpawnProxy(context.Background(), egressproxy.DriverCodex, netJob, nil, ProxyJournal(log, el))
	if err != nil {
		t.Fatalf("StartSpawnProxy: %v", err)
	}
	addr, _ := proxyAddrAuth(t, p)
	if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err != nil {
		t.Fatalf("a live proxy must accept: %v", err)
	} else {
		_ = c.Close()
	}
	if err := p.Close(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
		_ = c.Close()
		t.Fatalf("a CONNECT to the released port %s connected, want a refusal", addr)
	}
}
