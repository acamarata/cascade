package conversation

// Purpose: proves rpcCodeFor reflects the SAME table
// internal/rpc.Registry.Dispatch uses for every one of this ticket's four
// new sentinels, and that mapAdapterError is a true identity
// pass-through (never rewrites Kind, message, or type).
// SPORT: internal.conversation.adapter_errors/ADDED (tests) (P1-E20-W5-S43-T2).

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRPCCodeFor_NewSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"ErrMalformedTurnPayload", ErrMalformedTurnPayload, cascade.RPCCodeInvalidInput},
		{"ErrEgressSubstitutionFailed", ErrEgressSubstitutionFailed, cascade.RPCCodeUnavailable},
		{"ErrSSEWriteFailed", ErrSSEWriteFailed, cascade.RPCCodeUnavailable},
		{"ErrSSEUnavailableOnEmbedded", ErrSSEUnavailableOnEmbedded, cascade.RPCCodeUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rpcCodeFor(tc.err); got != tc.want {
				t.Errorf("rpcCodeFor(%s) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

func TestMapAdapterError_Identity(t *testing.T) {
	if mapAdapterError(nil) != nil {
		t.Fatalf("mapAdapterError(nil) must stay nil")
	}
	if got := mapAdapterError(ErrMalformedTurnPayload); got != ErrMalformedTurnPayload {
		t.Fatalf("mapAdapterError changed the error identity: got %v", got)
	}
}
