//go:build windows

// Purpose: windows's own thin entry point over sse.go's portable
//   RefuseSSEOnEmbedded, mirroring internal/fleet/resume/platform_windows.go's
//   exact precedent -- the parameter-driven check lives in sse.go so it
//   compiles and is unit-tested on every platform (Go treats any
//   "_windows.go"-suffixed file as implicitly windows-only regardless of
//   an explicit tag, which would hide a portable check from darwin/linux
//   CI if the logic lived only here).
// Inputs: none.
// Outputs: ErrSSEUnavailableOnEmbedded.
// Constraints: real code, no CASCADE-ALLOW, no stub.
// SPORT: internal.conversation.sse/ADDED (P1-E20-W5-S43-T2).

package conversation

// RefuseSSEEmbedded is Windows tier-2's sole SSE-mirror entry point: the
// platform has no daemon and therefore no events.Bus at all
// (06-FORGE-SPEC §2), so a composition root on this platform never even
// constructs an EventBus/Substitutor pair to pass to emitTurnAppended --
// it calls this instead and gets the same typed refusal a call through
// RefuseSSEOnEmbedded(ModeEmbedded) would reach.
func RefuseSSEEmbedded() error {
	return ErrSSEUnavailableOnEmbedded
}
