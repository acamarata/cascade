package egressproxy

// Purpose: the production upstream dialer and its resolved-address guard.
//
// Constraints: the allow list is matched by name and never resolved; the
// guard runs on the address the dialer actually connects to, after
// resolution and before connect(2), so a listed name that resolves (or is
// rebound) into loopback, link-local, private, shared or multicast space is
// refused without a packet leaving for it.

import (
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// dialTimeout bounds one upstream dial.
const dialTimeout = 10 * time.Second

// errForbiddenAddress is the guard's identity sentinel; dialReason matches
// it by identity, never by Kind.
var errForbiddenAddress = cascade.New(cascade.KindPolicyDenied,
	"egressproxy: the resolved upstream address is in a forbidden range")

// forbiddenPrefixes are refused in addition to the netip class checks.
var forbiddenPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // shared address space (CGNAT)
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, incl. broadcast
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 can embed a private IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2002::/16"),      // 6to4 can embed a private IPv4
	netip.MustParsePrefix("::/96"),          // IPv4-compatible (deprecated)
}

// ProductionDial is the DialFunc the spawn shim passes in production: a
// net.Dialer with a 10 s timeout whose Control hook refuses forbidden
// resolved addresses.
func ProductionDial() DialFunc {
	d := &net.Dialer{Timeout: dialTimeout, Control: refuseForbiddenAddress}
	return d.DialContext
}

// refuseForbiddenAddress is a net.Dialer Control hook. address is the
// resolved "ip:port" about to be connected; anything that is not a plain
// TCP connect to a public unicast address is refused.
func refuseForbiddenAddress(network, address string, _ syscall.RawConn) error {
	if network != "tcp4" && network != "tcp6" && network != "tcp" {
		return cascade.Wrapf(cascade.KindPolicyDenied, errForbiddenAddress, "network %q", network)
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return cascade.Wrapf(cascade.KindPolicyDenied, errForbiddenAddress, "unparseable address %q", address)
	}
	if forbiddenAddr(ap.Addr()) {
		return cascade.Wrapf(cascade.KindPolicyDenied, errForbiddenAddress, "address %s", ap.Addr())
	}
	return nil
}

// forbiddenAddr reports whether a is outside public unicast space.
func forbiddenAddr(a netip.Addr) bool {
	a = a.Unmap()
	if a.Zone() != "" || !a.IsValid() {
		return true
	}
	if a.IsLoopback() || a.IsUnspecified() || a.IsPrivate() || a.IsMulticast() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range forbiddenPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
