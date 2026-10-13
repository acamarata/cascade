// Purpose: tests for the R-21.158 protocol pin: range intersection,
//
//	runtime-only ErrHarnessIncompatible on empty intersection, and the
//	session-token frame authentication that rejects before parsing.
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) — P1-E30-W6-S61-T1, S61-T2.
package provider_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
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

// TestEncodeCallbackFrameRoundTrip encodes a frame, splits it the way the
// driver does, authenticates it with the same token and decodes the
// payload with unknown fields refused; the result must equal the input.
// A zero token and a foreign token both refuse.
func TestEncodeCallbackFrameRoundTrip(t *testing.T) {
	var tok provider.SessionToken
	copy(tok[:], "0123456789abcdef")
	in := provider.CallbackPayload{Seq: 7, DedupKey: "k-7", Kind: "stdout", ToolName: "Read", DurationMS: 12,
		ExitCode: 3, Paths: []string{"src/a.go", "b.txt"}, ResultHash: strings.Repeat("ab", 32),
		DataClass: provider.DataClassRestricted}
	frame, err := provider.EncodeCallbackFrame(tok, in)
	if err != nil {
		t.Fatalf("EncodeCallbackFrame: %v", err)
	}
	if frame[len(frame)-1] != '\n' || bytes.Count(frame, []byte("\n")) != 1 {
		t.Fatalf("frame %q must be exactly one newline-terminated line", frame)
	}
	head, body, ok := bytes.Cut(bytes.TrimSuffix(frame, []byte("\n")), []byte(" "))
	if !ok || string(head) != hex.EncodeToString(tok[:]) || strings.ToLower(string(head)) != string(head) {
		t.Fatalf("frame head %q, want the 32 lowercase hex token", head)
	}
	var got provider.SessionToken
	if _, err := hex.Decode(got[:], head); err != nil {
		t.Fatal(err)
	}
	payload, err := provider.AuthenticateFrame(tok, provider.ControlFrame{Token: got, Payload: body})
	if err != nil {
		t.Fatalf("AuthenticateFrame(own frame): %v", err)
	}
	var out provider.CallbackPayload
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil || dec.More() {
		t.Fatalf("decoding the payload: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip\n%+v\nwant\n%+v", out, in)
	}
	var other provider.SessionToken
	copy(other[:], "fedcba9876543210")
	if _, err := provider.AuthenticateFrame(other, provider.ControlFrame{Token: got, Payload: body}); err == nil {
		t.Fatal("a frame encoded under one token authenticated under another")
	}
	if _, err := provider.EncodeCallbackFrame(provider.SessionToken{}, in); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("EncodeCallbackFrame(zero token) err = %v, want KindInvalidInput", err)
	}
}
