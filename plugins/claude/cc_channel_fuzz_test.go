package claude

// Purpose: FuzzCCChannelDecode drives decodeHandshake — the one parser this
// package runs over bytes a harness sends (its MCP initialize params) —
// and the startup negotiation built on it, over the seed corpus in
// testdata/fuzz/FuzzCCChannelDecode/{valid,truncated,malformed}.json
// (P1-E37-W8-S73-T1). No input may panic; an accepted handshake always
// pins a single revision, and only the captured one negotiates.

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func FuzzCCChannelDecode(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		rng, err := decodeHandshake(data)
		if err != nil {
			wantExactly(t, err, ErrHandshakeInvalid, "initialize request")
			return
		}
		if rng.Min != rng.Max || (rng.Min != channelRevision1 && rng.Min != "0") {
			t.Fatalf("decodeHandshake(%q) = %+v: not a single known-or-refused revision", data, rng)
		}
		calls, log := 0, &frameLog{}
		ch := newCCChannel("linux", supportedProbe(data, false, &calls), log.send)
		startErr := ch.Start(context.Background())
		if (rng.Min == channelRevision1) != (startErr == nil) {
			t.Fatalf("revision %q negotiated with err %v", rng.Min, startErr)
		}
		if startErr != nil && startErr != provider.ErrHarnessIncompatible {
			t.Fatalf("an out-of-range revision refused with %v, not ErrHarnessIncompatible", startErr)
		}
	})
}
