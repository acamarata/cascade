package egressproxy

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// wantForbidden asserts err carries the guard sentinel by identity and the
// guard's message.
func wantForbidden(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: admitted, want refused", what)
	}
	if dialReason(err) != ReasonResolvedAddressForbidden {
		t.Fatalf("%s: error %v does not carry the guard sentinel", what, err)
	}
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindPolicyDenied {
		t.Fatalf("%s: error %v is not KindPolicyDenied", what, err)
	}
	if !errors.Is(err, errForbiddenAddress) || errors.Unwrap(err) != errForbiddenAddress {
		t.Fatalf("%s: error %v does not wrap the sentinel directly", what, err)
	}
}

func TestRefuseForbiddenAddress(t *testing.T) {
	refused := []string{
		"127.0.0.1:443", "[::1]:443", "169.254.169.254:80", "0.0.0.0:443",
		"[fe80::1]:443", "224.0.0.1:443", "[fe80::1%en0]:443", "[::ffff:127.0.0.1]:443",
		"255.255.255.255:443", "[ff02::1]:443", "[64:ff9b::a00:1]:443", "not-an-address:443",
		"[::ffff:100.64.0.1]:443", "[::ffff:198.18.0.1]:443", "[::ffff:0.0.0.1]:443", "[::ffff:240.0.0.1]:443",
	}
	for _, a := range refused {
		wantForbidden(t, refuseForbiddenAddress("tcp4", a, nil), a)
	}
	wantForbidden(t, refuseForbiddenAddress("udp4", "93.184.216.34:443", nil), "udp network")
	for _, a := range []string{"93.184.216.34:443", "[2606:2800:220:1:248:1893:25c8:1946]:443"} {
		if err := refuseForbiddenAddress("tcp", a, nil); err != nil {
			t.Fatalf("%s: refused a public address: %v", a, err)
		}
	}
}

func TestRefusePrivateRanges(t *testing.T) {
	private := []string{
		"10.0.0.1:443", "10.255.255.254:443", "172.16.0.1:443", "172.31.255.254:443",
		"192.168.0.1:443", "192.168.255.254:443", "100.64.0.1:443", "100.127.255.254:443",
		"[fc00::1]:443", "[fdff:ffff::1]:443",
	}
	for _, a := range private {
		wantForbidden(t, refuseForbiddenAddress("tcp", a, nil), a)
	}
	for _, a := range []string{"172.32.0.1:443", "100.128.0.1:443", "11.0.0.1:443", "93.184.216.34:443"} {
		if err := refuseForbiddenAddress("tcp", a, nil); err != nil {
			t.Fatalf("%s: refused a public neighbour of a private range: %v", a, err)
		}
	}
	// Through the proxy: a listed name rebound into each private range is
	// refused 403 with a confirm/resolved-address-forbidden row, and no
	// upstream connection is ever handed to the tunnel.
	for _, a := range private {
		h := newHarness(t, []Destination{"api.example.test:443"})
		h.dialHook = func(_ context.Context, addr string) (io.ReadWriteCloser, error) {
			if err := refuseForbiddenAddress("tcp", a, nil); err != nil {
				return nil, err
			}
			return h.upstream(addr)
		}
		status := h.roundTrip(t, h.connect("api.example.test:443", h.auth()))
		if status != 403 {
			t.Fatalf("%s: status %d, want 403", a, status)
		}
		h.wantRows(t, row(PhaseIntent, "api.example.test:443", ReasonAllowed),
			row(PhaseConfirm, "api.example.test:443", ReasonResolvedAddressForbidden))
		if n := h.upstreams.Load(); n != 0 {
			t.Fatalf("%s: %d upstream connections opened, want 0", a, n)
		}
	}
	if got := statusFor(ReasonResolvedAddressForbidden); got != 403 {
		t.Fatalf("resolved-address-forbidden maps to %d, want 403", got)
	}
}

func TestDialReasonMatchesByIdentityNotKind(t *testing.T) {
	sameKind := cascade.New(cascade.KindPolicyDenied, "some other policy refusal")
	if dialReason(sameKind) != ReasonDialFailed {
		t.Fatal("an unrelated KindPolicyDenied error was classified as the address guard")
	}
	if dialReason(context.DeadlineExceeded) != ReasonDialFailed {
		t.Fatal("a timeout was classified as the address guard")
	}
	guard := refuseForbiddenAddress("tcp", "10.0.0.1:443", nil)
	if dialReason(errors.Join(io.EOF, guard)) != ReasonResolvedAddressForbidden {
		t.Fatal("the guard sentinel inside a join tree was missed")
	}
	if dialReason(errors.Join(io.EOF, sameKind)) != ReasonDialFailed {
		t.Fatal("a join tree without the sentinel was classified as the address guard")
	}
	if ProductionDial() == nil {
		t.Fatal("ProductionDial returned nil")
	}
}

func TestStatusForEveryReason(t *testing.T) {
	want := map[Reason]int{
		ReasonProxyAuthRequired: 407, ReasonMalformedDestination: 400, ReasonTunnelLimit: 503,
		ReasonDialFailed: 502, ReasonMethodNotConnect: 403, ReasonDestinationNotAllowed: 403,
		ReasonJournalUnavailable: 403, ReasonResolvedAddressForbidden: 403, ReasonAllowed: 403,
		ReasonConnected: 403, ReasonClosed: 403, Reason("unknown"): 403,
	}
	for r, s := range want {
		if got := statusFor(r); got != s {
			t.Fatalf("statusFor(%s) = %d, want %d", r, got, s)
		}
	}
}
