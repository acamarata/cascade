//go:build integration

package egressproxy

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDriverProxyEmptyAllowlistRefusesAll(t *testing.T) {
	r := startRig(context.Background(), t, nil, map[Destination]string{allowed: echoServer(t)})
	if got := r.status(t, connectHead(string(allowed), basic(r.p))); got != 403 {
		t.Fatalf("status %d, want 403", got)
	}
	r.wantRows(t, row(PhaseDecide, allowed, ReasonDestinationNotAllowed))
	r.wantNoDial(t)
}

func TestDriverProxyRefusesUnlistedDestination(t *testing.T) {
	echo := echoServer(t)
	r := startRig(context.Background(), t, DestinationAllowlist{allowed},
		map[Destination]string{"evilapi.example.test:443": echo, "api.example.test.evil.test:443": echo})
	for _, target := range []Destination{"evilapi.example.test:443", "api.example.test.evil.test:443"} {
		if got := r.status(t, connectHead(string(target), basic(r.p))); got != 403 {
			t.Fatalf("%s: status %d, want 403", target, got)
		}
	}
	r.wantRows(t, row(PhaseDecide, "evilapi.example.test:443", ReasonDestinationNotAllowed),
		row(PhaseDecide, "api.example.test.evil.test:443", ReasonDestinationNotAllowed))
	r.wantNoDial(t)
}

func TestDriverProxyRefusesAllowedHostOtherPort(t *testing.T) {
	r := startRig(context.Background(), t, DestinationAllowlist{allowed},
		map[Destination]string{"api.example.test:8443": echoServer(t)})
	if got := r.status(t, connectHead("api.example.test:8443", basic(r.p))); got != 403 {
		t.Fatalf("status %d, want 403", got)
	}
	r.wantRows(t, row(PhaseDecide, "api.example.test:8443", ReasonDestinationNotAllowed))
	r.wantNoDial(t)
}

func TestDriverProxyRefusesIPLiteralOfAllowedName(t *testing.T) {
	// The injected resolver says api.example.test is 127.0.0.1; the proxy
	// must still refuse the literal, because membership is by name only.
	echo := echoServer(t)
	resolver := map[string]string{"api.example.test": "127.0.0.1"}
	r := startRig(context.Background(), t, DestinationAllowlist{allowed},
		map[Destination]string{allowed: echo, Destination(resolver["api.example.test"] + ":443"): echo})
	if got := r.status(t, connectHead("127.0.0.1:443", basic(r.p))); got != 403 {
		t.Fatalf("status %d, want 403", got)
	}
	r.wantRows(t, row(PhaseDecide, "127.0.0.1:443", ReasonDestinationNotAllowed))
	r.wantNoDial(t)
}

func TestDriverProxyRefusesPlainHTTP(t *testing.T) {
	r := startRig(context.Background(), t, DestinationAllowlist{allowed}, map[Destination]string{allowed: echoServer(t)})
	raw := "GET http://api.example.test:443/ HTTP/1.1\r\nHost: api.example.test:443\r\nProxy-Authorization: " + basic(r.p) + "\r\n\r\n"
	if got := r.status(t, raw); got != 403 {
		t.Fatalf("status %d, want 403", got)
	}
	r.wantRows(t, row(PhaseDecide, "", ReasonMethodNotConnect))
	r.wantNoDial(t)
}

func TestDriverProxyRefusesMalformedDestination(t *testing.T) {
	r := startRig(context.Background(), t, DestinationAllowlist{allowed}, map[Destination]string{allowed: echoServer(t)})
	targets := []string{
		"api.example.test", "*.example.test:443", "u@api.example.test:443",
		"api.example.test.:443", "api.example.test:0443", "http://api.example.test:443/",
	}
	var want []Decision
	for _, target := range targets {
		if got := r.status(t, connectHead(target, basic(r.p))); got != 400 {
			t.Fatalf("%q: status %d, want 400", target, got)
		}
		want = append(want, row(PhaseDecide, "", ReasonMalformedDestination))
	}
	r.wantRows(t, want...)
	r.wantNoDial(t)
}

func TestDriverProxyJournalFailureRefuses(t *testing.T) {
	r := startRig(context.Background(), t, DestinationAllowlist{allowed}, map[Destination]string{allowed: echoServer(t)})
	r.fail = func(d Decision) error {
		if d.Phase == PhaseIntent {
			return cascade.New(cascade.KindUnavailable, "audit log unavailable")
		}
		return nil
	}
	if got := r.status(t, connectHead(string(allowed), basic(r.p))); got != 403 {
		t.Fatalf("status %d, want 403", got)
	}
	r.wantRows(t, row(PhaseIntent, allowed, ReasonAllowed), row(PhaseDecide, allowed, ReasonJournalUnavailable))
	r.wantNoDial(t)
}

func TestDriverProxyRequiresCredential(t *testing.T) {
	routes := map[Destination]string{allowed: echoServer(t)}
	r := startRig(context.Background(), t, DestinationAllowlist{allowed}, routes)
	other := startRig(context.Background(), t, DestinationAllowlist{allowed}, routes)
	for _, auth := range []string{"", basic(other.p)} {
		if got := r.status(t, connectHead(string(allowed), auth)); got != 407 {
			t.Fatalf("auth %t: status %d, want 407", auth != "", got)
		}
	}
	r.wantRows(t, row(PhaseDecide, "", ReasonProxyAuthRequired), row(PhaseDecide, "", ReasonProxyAuthRequired))
	r.wantNoDial(t)
	other.wantNoDial(t)
}
