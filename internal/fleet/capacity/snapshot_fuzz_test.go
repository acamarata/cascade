package capacity

// Purpose (this file): FuzzSnapshotDecode - proves the two decoders a
// remote client of fleet.capacity/fleet.capacity_changed actually uses
// never panic on adversarial input: decoding a FleetSnapshot (the
// fleet.capacity JSON-RPC result) and decoding a changedPayload (the
// fleet.capacity_changed SSE delta). MethodFleetCapacity itself takes no
// params (Handler ignores raw params entirely - see rpc.go), so there is
// no request-params decoder to fuzz on this method; both real decode
// paths this ticket owns are exercised instead (06-FORGE-SPEC §5 rule 7).
// Seed corpus: testdata/fuzz/FuzzSnapshotDecode/seed1 (see its own header).

import (
	"encoding/json"
	"testing"
)

func FuzzSnapshotDecode(f *testing.F) {
	f.Add([]byte(`{"generated_at":"2026-09-11T12:00:00Z","seq":1,"providers":{},"nodes":{}}`))
	f.Add([]byte(`{"delta":{"changed_providers":{"acme":{"state":"exhausted"}}},"seq":2}`))
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"seq":-1}`))

	f.Fuzz(func(_ *testing.T, data []byte) {
		var snap FleetSnapshot
		_ = json.Unmarshal(data, &snap) // must never panic, error is fine

		var payload changedPayload
		_ = json.Unmarshal(data, &payload) // must never panic, error is fine
	})
}
