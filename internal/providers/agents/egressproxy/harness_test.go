package egressproxy

// In-memory harness for the untagged unit lane: connections are io.Pipe
// pairs, the proxy is built unbound by newProxy, and serveConn is driven
// directly. No socket is opened.

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pipeEnd is one end of an in-memory duplex.
type pipeEnd struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (e *pipeEnd) Read(b []byte) (int, error)  { return e.r.Read(b) }
func (e *pipeEnd) Write(b []byte) (int, error) { return e.w.Write(b) }
func (e *pipeEnd) Close() error {
	_ = e.r.Close()
	return e.w.Close()
}

// duplex returns two connected ends.
func duplex() (*pipeEnd, *pipeEnd) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return &pipeEnd{r: ar, w: aw}, &pipeEnd{r: br, w: bw}
}

// harness drives one unbound proxy.
type harness struct {
	p          *Proxy
	mu         sync.Mutex
	rows       []Decision
	notify     chan struct{}
	journalErr func(Decision) error
	dialHook   func(ctx context.Context, addr string) (io.ReadWriteCloser, error)
	dials      atomic.Int32
	upstreams  atomic.Int32
}

// newHarness builds an unbound proxy for DriverClaude / job-7.
func newHarness(t *testing.T, allow []Destination, mods ...func(*settings)) *harness {
	t.Helper()
	cfg := defaultSettings()
	for _, m := range mods {
		m(&cfg)
	}
	h := &harness{notify: make(chan struct{}, 1024)}
	p, err := newProxy(Options{DriverID: DriverClaude, JobID: "job-7", Allow: allow, Dial: ProductionDial(), Journal: h.journal}, cfg)
	if err != nil {
		t.Fatalf("newProxy: %v", err)
	}
	p.dial = func(ctx context.Context, addr string) (io.ReadWriteCloser, error) {
		h.dials.Add(1)
		if h.dialHook != nil {
			return h.dialHook(ctx, addr)
		}
		return h.upstream(addr)
	}
	h.p = p
	t.Cleanup(func() { _ = p.Close() })
	return h
}

// journal records d, then applies journalErr.
func (h *harness) journal(_ context.Context, d Decision) error {
	h.mu.Lock()
	h.rows = append(h.rows, d)
	h.mu.Unlock()
	h.notify <- struct{}{}
	if h.journalErr != nil {
		return h.journalErr(d)
	}
	return nil
}

// upstream opens an in-memory echo upstream.
func (h *harness) upstream(string) (io.ReadWriteCloser, error) {
	h.upstreams.Add(1)
	near, far := duplex()
	go func() {
		_, _ = io.Copy(far, far)
		_ = far.Close()
	}()
	return near, nil
}

// snapshot copies the recorded rows.
func (h *harness) snapshot() []Decision {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Decision(nil), h.rows...)
}

// waitRows blocks until at least n rows exist.
func (h *harness) waitRows(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for len(h.snapshot()) < n {
		select {
		case <-h.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %d rows, have %+v", n, h.snapshot())
		}
	}
}

// wantRows asserts the stored rows equal want exactly.
func (h *harness) wantRows(t *testing.T, want ...Decision) {
	t.Helper()
	h.waitRows(t, len(want))
	got := h.snapshot()
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// row is the Decision this harness's proxy should store.
func row(phase Phase, dest Destination, reason Reason) Decision {
	allowed := reason == ReasonAllowed || reason == ReasonConnected || reason == ReasonClosed
	return Decision{DriverID: DriverClaude, JobID: "job-7", Destination: dest, Phase: phase, Allowed: allowed, Reason: reason}
}

// auth is this proxy's valid Proxy-Authorization value.
func (h *harness) auth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(h.p.cred))
}

// connect builds a CONNECT head; an empty auth omits the header.
func (h *harness) connect(target, auth string) string {
	head := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if auth != "" {
		head += "Proxy-Authorization: " + auth + "\r\n"
	}
	return head + "\r\n"
}

// open serves one in-memory connection, writes raw, and reads the status.
func (h *harness) open(t *testing.T, raw string) (*pipeEnd, *bufio.Reader, int) {
	t.Helper()
	client, server := duplex()
	if !h.p.track(server) {
		t.Fatal("proxy refused to track a connection")
	}
	h.p.wg.Add(1)
	go h.p.serveConn(server)
	go func() { _, _ = io.WriteString(client, raw) }()
	br := bufio.NewReader(client)
	return client, br, readStatus(t, br)
}

// roundTrip is open plus close; it returns the status.
func (h *harness) roundTrip(t *testing.T, raw string) int {
	t.Helper()
	client, _, status := h.open(t, raw)
	_ = client.Close()
	return status
}

// readStatus parses the response status line and skips the head. A
// connection closed before any response is status 0.
func readStatus(t *testing.T, br *bufio.Reader) int {
	t.Helper()
	line, err := br.ReadString('\n')
	if err != nil {
		return 0
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("malformed status line %q", line)
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("malformed status line %q", line)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil || l == "\r\n" {
			return code
		}
	}
}
