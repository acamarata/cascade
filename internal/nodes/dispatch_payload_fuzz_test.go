package nodes

// Purpose (this file): DecodePayload never panics, refuses only with
//   KindInvalidInput, and anything it accepts is fenced to its own
//   dispatch attempt and hashes to its Digest.
// SPORT: internal/nodes (ADD fuzz) — P1-NODE-02b.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FuzzDispatchPayload feeds DecodePayload arbitrary bytes seeded from
// valid and near-valid payloads.
func FuzzDispatchPayload(f *testing.F) {
	good := validPayload(f)
	b := mustEncode(f, good)
	f.Add(b)
	resumed := good
	resumed.Resume = ResumePoint{EntityID: "job-1", CompletedOperations: []string{"op-a"}}
	f.Add(mustEncode(f, resumed))
	f.Add(bytes.Replace(b, []byte(`"attempt":2`), []byte(`"attempt":3`), 1))
	f.Add(bytes.Replace(b, []byte(`{"v":1,`), []byte(`{"v":1,"x":0,`), 1))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"envelope":{"env":{},"sig":""}}`))
	f.Add([]byte(`[`))

	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := DecodePayload(data)
		if err != nil {
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Fatalf("refusal kind = %v (%v), want KindInvalidInput", kind, err)
			}
			return
		}
		fence := p.Envelope.Env.Fence
		if fence.DispatchID != p.DispatchID || fence.Attempt != p.Attempt {
			t.Fatalf("accepted a payload whose fence %+v is not (%q, %d)", fence, p.DispatchID, p.Attempt)
		}
		sum := sha256.Sum256(data)
		if p.Digest() != hex.EncodeToString(sum[:]) {
			t.Fatal("accepted bytes do not hash to the payload digest")
		}
	})
}
