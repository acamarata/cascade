package provider

// Purpose: R-21.158 protocol pins — the vendor-defined ProtocolVersion an
//
//	adapter and a harness negotiate ONCE at startup, pinned for the life
//	of a job, plus the per-spawn session token that authenticates the
//	control channel a driver's frames arrive on, and the callback contract
//	(R-14.327 item 5) its one producer, the hook relay, reuses and never
//	restates: CallbackFileName, CallbackFile, CallbackPayload and
//	EncodeCallbackFrame.
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
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-E30-W6-S61-T1, S61-T2).

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

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
	if !want.Valid() || subtle.ConstantTimeCompare(frame.Token[:], want[:]) != 1 {
		return nil, cascade.New(cascade.KindPermissionDenied, "provider: control frame missing or invalid session token")
	}
	return frame.Payload, nil
}

// CallbackFileName is the file a driver writes (O_EXCL, mode 0600) under
// the leased tree's .claude/ directory to announce its callback channel.
const CallbackFileName = "cascade-callback.json"

// CallbackFile is CallbackFileName's JSON body: the unix socket path (in a
// fresh 0700 directory outside the leased tree) and the session token as
// 32 lowercase hex digits.
type CallbackFile struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
}

// CallbackPayload is the closed field set of one callback frame; a driver
// decodes it with unknown fields refused. Kind is one of stdout, status,
// error or done; Seq and DedupKey order and de-duplicate frames.
type CallbackPayload struct {
	Seq        uint64    `json:"seq"`
	DedupKey   string    `json:"dedup_key"`
	Kind       string    `json:"kind"`
	ToolName   string    `json:"tool_name"`
	DurationMS int64     `json:"duration_ms"`
	ExitCode   int       `json:"exit_code"`
	Paths      []string  `json:"paths"`
	ResultHash string    `json:"result_hash"`
	DataClass  DataClass `json:"data_class"`
}

// EncodeCallbackFrame returns one frame: the token as 32 lowercase hex
// digits, a space, the payload JSON and a newline. A zero token refuses
// (a frame nobody can authenticate is never produced).
//
// SDK-INTENT: the hook relay producer (AD/S-62.T3) is the one production
// producer; until it lands only tests encode frames.
func EncodeCallbackFrame(tok SessionToken, p CallbackPayload) ([]byte, error) {
	if !tok.Valid() {
		return nil, cascade.New(cascade.KindInvalidInput, "provider: callback frame needs a non-zero session token")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "provider: encoding the callback payload")
	}
	out := make([]byte, 0, 2*len(tok)+len(body)+2)
	out = hex.AppendEncode(out, tok[:])
	out = append(out, ' ')
	out = append(out, body...)
	return append(out, '\n'), nil
}
