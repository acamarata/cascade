package antigravity

// Purpose: the antigravity protocol pin: protocol revision 1 (the
// stream-json event set recorded in testdata/stream-json), the pinned agy
// CLI version, and the two distinct refusals a driver raises before or
// while reading a child.
// Inputs: a peer ProtocolRange, or the CLI version string and help text.
// Outputs: the negotiated revision, or a typed refusal.
// Constraints: a version or event-set mismatch is provider.ErrHarnessIncompatible
// (KindConflict, the one runtime refusal); a CLI without the stream-json
// output interface is ErrInterfaceAbsent (KindUnsupported). The two never
// alias: callers compare identity.
// SPORT: providers.agents.antigravity/ADD (P1-AGT-07).

import (
	"bytes"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ProtocolRevision is the only stream-json revision this package decodes;
// testdata/stream-json/agy-1.4.12-print.jsonl defines it.
const ProtocolRevision provider.ProtocolVersion = "1"

// PinnedAgyVersion is the agy CLI version the revision 1 fixture was
// recorded against.
const PinnedAgyVersion = "1.4.12"

// streamJSONFlag is the help-text token proving the stream-json output
// interface exists.
const streamJSONFlag = "--output-format"

var ourProtocolRange = provider.ProtocolRange{Min: ProtocolRevision, Max: ProtocolRevision}

// ErrInterfaceAbsent refuses a CLI or child output that lacks the
// stream-json interface entirely (no --output-format stream-json, or a
// stream that does not open with an init event). It is distinct from
// provider.ErrHarnessIncompatible, which means the interface exists at the
// wrong version.
var ErrInterfaceAbsent = cascade.New(cascade.KindUnsupported, "antigravity: stream-json output interface absent")

// SupportedProtocols returns the {1,1} range.
func SupportedProtocols() provider.ProtocolRange { return ourProtocolRange }

// Negotiate pins one revision from the intersection with peer, or returns
// provider.ErrHarnessIncompatible.
func Negotiate(peer provider.ProtocolRange) (provider.ProtocolVersion, error) {
	return provider.NegotiateProtocol(ourProtocolRange, peer)
}

// CheckHarness pins the installed CLI: help must offer stream-json output
// (else ErrInterfaceAbsent) and version must equal PinnedAgyVersion (else
// provider.ErrHarnessIncompatible). Both sentinels are returned bare so
// identity holds.
func CheckHarness(version string, help []byte) error {
	if !bytes.Contains(help, []byte(streamJSONFlag)) || !bytes.Contains(help, []byte("stream-json")) {
		return ErrInterfaceAbsent
	}
	if normalizeVersion(version) != PinnedAgyVersion {
		return provider.ErrHarnessIncompatible
	}
	return nil
}

// normalizeVersion takes the first field and drops a leading "v" or a
// trailing ':' (agy changelog headings read "1.4.12:").
func normalizeVersion(v string) string {
	f := strings.Fields(v)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(f[0], "v"), ":")
}
