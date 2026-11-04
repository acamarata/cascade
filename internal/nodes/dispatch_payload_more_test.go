package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestEncodePayloadRefusesWhatDecodeWouldRefuse(t *testing.T) {
	short := validPayload(t)
	short.Envelope.Sig = short.Envelope.Sig[:10]
	_, err := EncodePayload(short)
	wantInvalid(t, "short signature", err)
	if err == nil || !strings.Contains(err.Error(), "signature has length 10") {
		t.Errorf("short signature error = %v, want the length named", err)
	}

	badVersion := validPayload(t)
	badVersion.Version = PayloadVersion + 1
	_, err = EncodePayload(badVersion)
	wantInvalid(t, "bad version", err)

	badEnv := validPayload(t)
	badEnv.Envelope.Env.Version = 9999
	_, err = EncodePayload(badEnv)
	wantInvalid(t, "uncanonical envelope", err)
}

func TestPayloadDigestIsSha256OfWireBytesOrEmpty(t *testing.T) {
	p := validPayload(t)
	sum := sha256.Sum256(mustEncode(t, p))
	if got := p.Digest(); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("Digest = %q, want sha256 of the wire bytes", got)
	}
	p.Attempt = 0
	if got := p.Digest(); got != "" {
		t.Fatalf("Digest of an unencodable payload = %q, want empty", got)
	}
}

func TestDispatchIDMustBeOneRefSegment(t *testing.T) {
	long := strings.Repeat("x", 100)
	for _, id := range []string{"has space", "tab\tid", "del\x7fid", "a..b", "x.lock", "a/b", long + "/"} {
		p := validPayload(t)
		p.DispatchID = id
		_, err := EncodePayload(p)
		wantInvalid(t, "dispatch id "+clipText(id), err)
		if err != nil && len(err.Error()) > 400 {
			t.Errorf("error for a long id is %d bytes; the quoted text must be clipped", len(err.Error()))
		}
	}
	if got := clipText(long); got != long[:64]+"..." {
		t.Errorf("clipText(100 chars) = %q", got)
	}
	if got := clipText("short"); got != "short" {
		t.Errorf("clipText(short) = %q", got)
	}
}
