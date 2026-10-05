// Package egressproxy is the per-spawn loopback CONNECT proxy an external
// agent driver is pointed at through its proxy environment.
//
// Purpose: one proxy per driver spawn admits only exact "host:port"
// members of that driver's [agents.egress.<driver-id>] allow list, demands
// a per-spawn proxy credential, journals every decision through an injected
// seam, and produces the exact environment pairs the spawn shim sets.
//
// Constraints: advisory routing only. A driver that ignores its proxy
// environment is not contained by this package; OS-enforced containment is
// the DEF-P2-driver-netns deferral. Membership is exact string equality
// after normalization: no wildcard, suffix or CIDR entry, and no name is
// ever resolved to compare addresses. Tunneled bytes are copied, never
// read, logged or journaled.
//
// SPORT: contract:driver-egress-proxy (cap:egress-policy, cap:agent-drivers).
package egressproxy

import (
	"net/netip"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DriverID names one member of the closed external-driver set.
type DriverID string

// The closed driver set. A config table or an Options value naming any
// other id is refused.
const (
	DriverClaude      DriverID = "claude"
	DriverCodex       DriverID = "codex"
	DriverOpenCode    DriverID = "opencode"
	DriverAntigravity DriverID = "antigravity"
)

// knownDrivers is the closed set in a stable order.
var knownDrivers = []DriverID{DriverClaude, DriverCodex, DriverOpenCode, DriverAntigravity}

// known reports whether id is a member of the closed driver set.
func (id DriverID) known() bool {
	for _, k := range knownDrivers {
		if id == k {
			return true
		}
	}
	return false
}

// Destination is a normalized "host:port" authority.
type Destination string

// DestinationAllowlist holds exact members only.
type DestinationAllowlist []Destination

// maxNameLen is the DNS limit on a presentation-form name without the
// trailing dot.
const maxNameLen = 253

// ParseDestination validates s as a canonical "host:port" authority: a
// lowercase ASCII DNS name, an IPv4 literal, or a bracketed IPv6 literal
// without a zone, then a decimal port 1-65535 without leading zeros.
// Wildcards, schemes, paths, userinfo, a trailing dot and a missing port
// are refused with KindInvalidInput.
func ParseDestination(s string) (Destination, error) {
	host, port, ok := splitAuthority(s)
	if !ok || !validPort(port) {
		return "", invalidDestination(s)
	}
	if strings.HasPrefix(host, "[") {
		if !validIPv6Literal(host) {
			return "", invalidDestination(s)
		}
		return Destination(s), nil
	}
	if validIPv4Literal(host) || validName(host) {
		return Destination(s), nil
	}
	return "", invalidDestination(s)
}

// invalidDestination builds the refusal for one entry. The text quotes the
// entry because it is operator config, never a request target.
func invalidDestination(s string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"egressproxy: %q is not a lowercase host:port destination", s)
}

// splitAuthority splits s at its port colon. A bracketed host keeps its
// brackets; an unbracketed host may not contain a colon.
func splitAuthority(s string) (host, port string, ok bool) {
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]:")
		if end < 0 {
			return "", "", false
		}
		return s[:end+1], s[end+2:], true
	}
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || strings.IndexByte(s[:i], ':') >= 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// validPort accepts decimal 1-65535 with no sign and no leading zero.
func validPort(p string) bool {
	if p == "" || len(p) > 5 || p[0] == '0' {
		return false
	}
	n := 0
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
		n = n*10 + int(p[i]-'0')
	}
	return n <= 65535
}

// validIPv4Literal accepts only canonical dotted-quad IPv4.
func validIPv4Literal(h string) bool {
	a, err := netip.ParseAddr(h)
	return err == nil && a.Is4() && a.String() == h
}

// validIPv6Literal accepts "[addr]" where addr is canonical IPv6 text with
// no zone and is not an IPv4 address in disguise. h comes from
// splitAuthority, which guarantees the surrounding brackets.
func validIPv6Literal(h string) bool {
	inner := h[1 : len(h)-1]
	a, err := netip.ParseAddr(inner)
	if err != nil || !a.Is6() || a.Is4In6() || a.Zone() != "" {
		return false
	}
	return a.String() == inner
}

// validName accepts a lowercase LDH DNS name with no trailing dot whose
// final label starts with a letter, so numeric shorthands such as "127.1"
// that a resolver may read as an address are refused.
func validName(h string) bool {
	if h == "" || len(h) > maxNameLen {
		return false
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if !validLabel(l) {
			return false
		}
	}
	last := labels[len(labels)-1]
	return last[0] >= 'a' && last[0] <= 'z'
}

// validLabel accepts 1-63 of [a-z0-9-], not starting or ending with '-'.
func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// normalizeTarget lowercases ASCII letters in a request target and refuses
// any non-ASCII byte, so a Unicode case fold can never turn a foreign name
// into an allowed one. The result still has to pass ParseDestination.
func normalizeTarget(raw string) (Destination, bool) {
	b := []byte(raw)
	for i, c := range b {
		if c >= 0x80 {
			return "", false
		}
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	d, err := ParseDestination(string(b))
	return d, err == nil
}

// allowSet is the exact-membership form of an allow list.
type allowSet map[Destination]struct{}

// newAllowSet re-validates every entry, so a Destination built by a bare
// conversion instead of ParseDestination cannot widen the set.
func newAllowSet(list DestinationAllowlist) (allowSet, error) {
	set := make(allowSet, len(list))
	for _, d := range list {
		if _, err := ParseDestination(string(d)); err != nil {
			return nil, err
		}
		set[d] = struct{}{}
	}
	return set, nil
}

// contains reports exact membership. An empty set admits nothing.
func (s allowSet) contains(d Destination) bool {
	_, ok := s[d]
	return ok
}
