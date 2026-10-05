package egressproxy

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

const allowed Destination = "api.example.test:443"

func TestServeConnDecisionOrder(t *testing.T) {
	other := "Basic " + base64.StdEncoding.EncodeToString([]byte(proxyUser+":"+strings.Repeat("0", 64)))
	cases := []struct {
		name   string
		raw    func(h *harness) string
		status int
		want   Decision
	}{
		{"no credential", func(h *harness) string { return h.connect(string(allowed), "") }, 407, row(PhaseDecide, "", ReasonProxyAuthRequired)},
		{"wrong credential", func(h *harness) string { return h.connect(string(allowed), other) }, 407, row(PhaseDecide, "", ReasonProxyAuthRequired)},
		{"bearer scheme", func(h *harness) string { return h.connect(string(allowed), "Bearer "+h.p.cred) }, 407, row(PhaseDecide, "", ReasonProxyAuthRequired)},
		{"bad base64", func(h *harness) string { return h.connect(string(allowed), "Basic %%%") }, 407, row(PhaseDecide, "", ReasonProxyAuthRequired)},
		{"no auth beats bad method", func(_ *harness) string { return "GET http://api.example.test:443/ HTTP/1.1\r\nHost: x\r\n\r\n" }, 407, row(PhaseDecide, "", ReasonProxyAuthRequired)},
		{"two credentials", func(h *harness) string {
			return "CONNECT " + string(allowed) + " HTTP/1.1\r\nHost: x\r\nProxy-Authorization: " + h.auth() + "\r\nProxy-Authorization: " + h.auth() + "\r\n\r\n"
		}, 407, row(PhaseDecide, "", ReasonProxyAuthRequired)},
		{"plain http", func(h *harness) string {
			return "GET http://api.example.test:443/ HTTP/1.1\r\nHost: api.example.test:443\r\nProxy-Authorization: " + h.auth() + "\r\n\r\n"
		}, 403, row(PhaseDecide, "", ReasonMethodNotConnect)},
		{"absolute-form connect", func(h *harness) string { return h.connect("http://api.example.test:443/", h.auth()) }, 400, row(PhaseDecide, "", ReasonMalformedDestination)},
		{"zero-padded port", func(h *harness) string { return h.connect("api.example.test:0443", h.auth()) }, 400, row(PhaseDecide, "", ReasonMalformedDestination)},
		{"ipv6 zone", func(h *harness) string { return h.connect("[fe80::1%25en0]:443", h.auth()) }, 400, row(PhaseDecide, "", ReasonMalformedDestination)},
		{"non-ascii fold", func(h *harness) string { return h.connect("api.example.tesK:443", h.auth()) }, 400, row(PhaseDecide, "", ReasonMalformedDestination)},
		{"unlisted", func(h *harness) string { return h.connect("evilapi.example.test:443", h.auth()) }, 403, row(PhaseDecide, "evilapi.example.test:443", ReasonDestinationNotAllowed)},
		{"host header ignored", func(h *harness) string {
			return "CONNECT evil.example.test:443 HTTP/1.1\r\nHost: " + string(allowed) + "\r\nProxy-Authorization: " + h.auth() + "\r\n\r\n"
		}, 403, row(PhaseDecide, "evil.example.test:443", ReasonDestinationNotAllowed)},
		{"garbage head", func(_ *harness) string { return "\x16\x03\x01 not http\r\n\r\n" }, 400, row(PhaseDecide, "", ReasonMalformedDestination)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, []Destination{allowed})
			if got := h.roundTrip(t, c.raw(h)); got != c.status {
				t.Fatalf("status %d, want %d", got, c.status)
			}
			h.wantRows(t, c.want)
			if n := h.dials.Load(); n != 0 {
				t.Fatalf("dialed %d times, want 0", n)
			}
		})
	}
}

func TestServeConnTunnelsInMemory(t *testing.T) {
	h := newHarness(t, []Destination{allowed})
	// Upper case is normalized; early bytes after the head ride the tunnel.
	client, br, status := h.open(t, h.connect("API.Example.TEST:443", h.auth())+"early")
	if status != 200 {
		t.Fatalf("status %d, want 200", status)
	}
	if _, err := io.WriteString(client, "-late"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("early-late"))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "early-late" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
	_ = client.Close()
	h.wantRows(t, row(PhaseIntent, allowed, ReasonAllowed), row(PhaseConfirm, allowed, ReasonConnected),
		row(PhaseConfirm, allowed, ReasonClosed))
	if n := h.dials.Load(); n != 1 {
		t.Fatalf("dialed %d times, want 1", n)
	}
}

func TestServeConnJournalFailuresRefuse(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "journal down")
	cases := []struct {
		name  string
		fail  func(Decision) error
		dials int32
		want  []Decision
	}{
		{"intent error", func(d Decision) error {
			if d.Phase == PhaseIntent {
				return boom
			}
			return nil
		}, 0, []Decision{row(PhaseIntent, allowed, ReasonAllowed), row(PhaseDecide, allowed, ReasonJournalUnavailable)}},
		{"intent panic", func(Decision) error { panic("journal exploded") }, 0,
			[]Decision{row(PhaseIntent, allowed, ReasonAllowed), row(PhaseDecide, allowed, ReasonJournalUnavailable)}},
		{"confirm error", func(d Decision) error {
			if d.Reason == ReasonConnected {
				return boom
			}
			return nil
		}, 1, []Decision{row(PhaseIntent, allowed, ReasonAllowed), row(PhaseConfirm, allowed, ReasonConnected), row(PhaseDecide, allowed, ReasonJournalUnavailable)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, []Destination{allowed})
			h.journalErr = c.fail
			if got := h.roundTrip(t, h.connect(string(allowed), h.auth())); got != 403 {
				t.Fatalf("status %d, want 403", got)
			}
			h.wantRows(t, c.want...)
			if n := h.dials.Load(); n != c.dials {
				t.Fatalf("dialed %d times, want %d", n, c.dials)
			}
		})
	}
}

func TestServeConnTunnelLimit(t *testing.T) {
	h := newHarness(t, []Destination{allowed}, func(s *settings) { s.maxTunnels = 2 })
	var open []*pipeEnd
	for i := 0; i < 2; i++ {
		c, _, status := h.open(t, h.connect(string(allowed), h.auth()))
		if status != 200 {
			t.Fatalf("tunnel %d: status %d", i, status)
		}
		open = append(open, c)
	}
	if got := h.roundTrip(t, h.connect(string(allowed), h.auth())); got != 503 {
		t.Fatalf("third tunnel status %d, want 503", got)
	}
	if n := h.dials.Load(); n != 2 {
		t.Fatalf("dialed %d times, want 2", n)
	}
	_ = open[0].Close()
	h.waitRows(t, 2*2+1+1) // two tunnels' intent+connected, the refusal, one closed
	c, _, status := h.open(t, h.connect(string(allowed), h.auth()))
	if status != 200 {
		t.Fatalf("after a tunnel closed: status %d, want 200", status)
	}
	_ = c.Close()
	_ = open[1].Close()
	if got := defaultSettings().maxTunnels; got != 64 {
		t.Fatalf("default tunnel cap = %d, want 64", got)
	}
}

func TestServeConnHeadLimits(t *testing.T) {
	h := newHarness(t, []Destination{allowed}, func(s *settings) { s.headerTimeout = 50 * time.Millisecond })
	// A slowloris head that never finishes is cut at the deadline.
	if got := h.roundTrip(t, "CONNECT api.example.test:443 HTTP/1.1\r\nX-Slow: "); got != 0 && got != 400 {
		t.Fatalf("slow head status %d, want a closed connection or 400", got)
	}
	h.wantRows(t, row(PhaseDecide, "", ReasonMalformedDestination))

	h2 := newHarness(t, []Destination{allowed}, func(s *settings) { s.maxHeaderBytes = 256 })
	big := h2.connect(string(allowed), h2.auth())
	big = strings.Replace(big, "\r\n\r\n", "\r\nX-Pad: "+strings.Repeat("a", 512)+"\r\n\r\n", 1)
	if got := h2.roundTrip(t, big); got != 400 {
		t.Fatalf("oversized head status %d, want 400", got)
	}
	h2.wantRows(t, row(PhaseDecide, "", ReasonMalformedDestination))
	if h.dials.Load()+h2.dials.Load() != 0 {
		t.Fatal("a refused head dialed")
	}
	def := defaultSettings()
	if def.headerTimeout != 10*time.Second || def.maxHeaderBytes != 32<<10 {
		t.Fatalf("default head limits = %v / %d", def.headerTimeout, def.maxHeaderBytes)
	}
}

func TestServeConnDialFailure(t *testing.T) {
	h := newHarness(t, []Destination{allowed})
	var bounded bool
	h.dialHook = func(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
		deadline, ok := ctx.Deadline()
		bounded = ok && time.Until(deadline) <= dialTimeout && time.Until(deadline) > dialTimeout-5*time.Second
		return nil, cascade.New(cascade.KindPolicyDenied, "not the guard")
	}
	if got := h.roundTrip(t, h.connect(string(allowed), h.auth())); got != 502 {
		t.Fatalf("status %d, want 502", got)
	}
	h.wantRows(t, row(PhaseIntent, allowed, ReasonAllowed), row(PhaseConfirm, allowed, ReasonDialFailed))
	if !bounded {
		t.Fatal("the injected Dial ran without the 10 s deadline")
	}
}
