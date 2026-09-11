// Purpose: tests for the R-21.158 protocol pin: range intersection,
//
//	runtime-only ErrHarnessIncompatible on empty intersection, and the
//	session-token frame authentication that rejects before parsing.
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) — P1-E30-W6-S61-T1.
package provider_test

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestProtocolNegotiateRefusesOutOfRange(t *testing.T) {
	mine := provider.ProtocolRange{Min: "v1", Max: "v2"}
	peer := provider.ProtocolRange{Min: "v5", Max: "v9"}
	if _, err := provider.NegotiateProtocol(mine, peer); err != provider.ErrHarnessIncompatible {
		t.Fatalf("NegotiateProtocol(disjoint) err = %v, want ErrHarnessIncompatible", err)
	}
}

func TestProtocolNegotiatePinsHighestOverlap(t *testing.T) {
	mine := provider.ProtocolRange{Min: "v1", Max: "v3"}
	peer := provider.ProtocolRange{Min: "v2", Max: "v9"}
	got, err := provider.NegotiateProtocol(mine, peer)
	if err != nil {
		t.Fatalf("NegotiateProtocol: %v", err)
	}
	if got != "v3" {
		t.Fatalf("NegotiateProtocol = %q, want v3 (the top of the overlap)", got)
	}
}

func TestAuthenticateFrameRejectsMissingToken(t *testing.T) {
	var want provider.SessionToken
	copy(want[:], "0123456789abcdef")
	frame := provider.ControlFrame{Payload: []byte("should never parse")}
	if _, err := provider.AuthenticateFrame(want, frame); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("AuthenticateFrame(no token) err = %v, want KindPermissionDenied", err)
	}
}

func TestAuthenticateFrameAcceptsMatchingToken(t *testing.T) {
	var want provider.SessionToken
	copy(want[:], "0123456789abcdef")
	frame := provider.ControlFrame{Token: want, Payload: []byte("payload")}
	got, err := provider.AuthenticateFrame(want, frame)
	if err != nil {
		t.Fatalf("AuthenticateFrame: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("AuthenticateFrame payload = %q, want %q", got, "payload")
	}
}

func TestAuthenticateFrameRejectsZeroWant(t *testing.T) {
	var zero provider.SessionToken
	if _, err := provider.AuthenticateFrame(zero, provider.ControlFrame{Token: zero}); err == nil {
		t.Fatal("AuthenticateFrame with a zero session token on both sides must still refuse")
	}
}
