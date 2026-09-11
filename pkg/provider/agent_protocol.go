package provider

// Purpose: R-21.158 protocol pins — the vendor-defined ProtocolVersion an
//
//	adapter and a harness negotiate ONCE at startup, pinned for the life
//	of a job, plus the per-spawn session token that authenticates the
//	control channel a driver's frames arrive on.
//
// Inputs: the adapter's and the harness's declared ProtocolRange.
// Outputs: NegotiateProtocol's pinned ProtocolVersion, or
//
//	ErrHarnessIncompatible when the ranges do not intersect.
//
// Constraints: ProtocolVersion is opaque (vendor-defined); this package
//
//	compares ranges by ordinary string ordering as a best-effort
//	total order — a vendor whose version strings do not sort consistently
//	with their real precedence gets an imprecise (but still safe: never
//	permissive on empty intersection) pin. AuthenticateFrame rejects an
//	unauthenticated frame before any parsing is attempted.
//
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-E30-W6-S61-T1).

import "github.com/acamarata/cascade/pkg/cascade"

// ProtocolVersion is an opaque, vendor-defined version string.
type ProtocolVersion string

// ProtocolRange is the [Min, Max] range an adapter declares from
// SupportedProtocols(), or a harness declares as its own supported range
// when calling Negotiate.
type ProtocolRange struct {
	Min ProtocolVersion
	Max ProtocolVersion
}

// intersect returns the overlap of a and b using ordinary string
// ordering over ProtocolVersion, and whether any overlap exists.
func (a ProtocolRange) intersect(b ProtocolRange) (ProtocolRange, bool) {
	lo := a.Min
	if b.Min > lo {
		lo = b.Min
	}
	hi := a.Max
	if b.Max < hi {
		hi = b.Max
	}
	if lo > hi {
		return ProtocolRange{}, false
	}
	return ProtocolRange{Min: lo, Max: hi}, true
}

// NegotiateProtocol pins ONE ProtocolVersion for the life of a job: the
// highest version in the intersection of mine and peer. An empty
// intersection returns ErrHarnessIncompatible — the one uniform RUNTIME
// refusal (R-21.158), never a build-time-only check.
func NegotiateProtocol(mine, peer ProtocolRange) (ProtocolVersion, error) {
	overlap, ok := mine.intersect(peer)
	if !ok {
		return "", ErrHarnessIncompatible
	}
	return overlap.Max, nil
}

// SessionToken is the 128-bit per-spawn token the host mints and the
// driver presents on the control channel; a frame without a matching
// token is rejected rather than parsed.
type SessionToken [16]byte

// Valid reports whether t is non-zero.
func (t SessionToken) Valid() bool {
	return t != SessionToken{}
}

// ControlFrame is one raw frame arriving on a driver's control channel,
// carrying the SessionToken it claims and an opaque payload.
type ControlFrame struct {
	Token   SessionToken
	Payload []byte
}

// AuthenticateFrame checks frame's token against want before any parsing
// is attempted. A mismatched or zero token is rejected with a typed error
// and frame.Payload is never returned.
func AuthenticateFrame(want SessionToken, frame ControlFrame) ([]byte, error) {
	if !want.Valid() || frame.Token != want {
		return nil, cascade.New(cascade.KindPermissionDenied, "provider: control frame missing or invalid session token")
	}
	return frame.Payload, nil
}
