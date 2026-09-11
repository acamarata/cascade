package supervision

// Purpose (this file): FuzzAttentionParams - proves the three JSON param
// decoders fleet.attention.list/get/ack actually use (decodeAttnParams
// against attnListParams/attnGetParams/attnAckParams) never panic on
// adversarial input, mirroring internal/fleet/capacity's
// FuzzSnapshotDecode precedent (06-FORGE-SPEC §5 rule 7). Seed corpus:
// testdata/fuzz/FuzzAttentionParams/seed1 (R-21.266: package-local, never
// internal/testdata/fuzz/).

import (
	"encoding/json"
	"testing"
)

func FuzzAttentionParams(f *testing.F) {
	f.Add([]byte(`{"all":true,"kind":"stall"}`))
	f.Add([]byte(`{"id":"x"}`))
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"id":123}`))
	f.Add([]byte(`{"bogus_field":true}`))

	f.Fuzz(func(_ *testing.T, data []byte) {
		var lp attnListParams
		_ = decodeAttnParams(data, &lp) // must never panic, error is fine

		var gp attnGetParams
		_ = decodeAttnParams(data, &gp)

		var ap attnAckParams
		_ = decodeAttnParams(data, &ap)

		// The wire result shapes must also survive adversarial decode,
		// since a malicious daemon peer (or a corrupted local store
		// record) can hand this JSON back to the Client.
		var lr attnListResult
		_ = json.Unmarshal(data, &lr)
		var gr attnGetResult
		_ = json.Unmarshal(data, &gr)
		var ar attnAckResult
		_ = json.Unmarshal(data, &ar)
	})
}
