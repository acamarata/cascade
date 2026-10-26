package rpc

// Purpose (this file): the envelope rule. A conditional verb must not run
//   because an `_attestation` or `_args` key made its rule read "narrowing",
//   nor because of key case or colliding keys.

import (
	"context"
	"encoding/json"
	"testing"
)

// TestConditionalEnvelopeCannotBypass: a conditional verb with a lenient
// handler must not run because the envelope makes the rule read "narrowing".
func TestConditionalEnvelopeCannotBypass(t *testing.T) {
	skipIfPlatformRefuses(t)
	for _, tc := range []struct{ name, params string }{
		{"empty attestation and args beside a loosening field", `{"_attestation":{},"_args":{},"direction":"loosen"}`},
		{"args only", `{"_args":{"direction":"loosen"}}`},
		{"attestation only", `{"_attestation":{}}`},
		{"narrowing direction outside the envelope", `{"_attestation":{},"_args":{"direction":"loosen"},"direction":"narrow"}`},
		{"upper-case envelope keys", `{"_ATTESTATION":{},"_ARGS":{"direction":"loosen"},"direction":"narrow"}`},
		{"upper-case direction key", `{"Direction":"loosen"}`},
		{"keys that fold together", `{"direction":"narrow","DIRECTION":"loosen"}`},
		{"null attestation with args", `{"_attestation":null,"_args":{"direction":"loosen"},"direction":"narrow"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMWFixture(t, "policy.set")
			_, err := f.wrapped(context.Background(), json.RawMessage(tc.params))
			if err == nil {
				t.Fatal("the call went through unattested")
			}
			if f.calls != 0 {
				t.Fatalf("lenient handler ran %d times", f.calls)
			}
		})
	}
	// A plain narrowing request, with no envelope, is still not elevated.
	f := newMWFixture(t, "policy.set")
	if _, err := f.wrapped(context.Background(), json.RawMessage(`{"direction":"narrow"}`)); err != nil || f.calls != 1 {
		t.Fatalf("narrowing without an envelope: err=%v calls=%d, want a plain pass-through", err, f.calls)
	}
	// An attested envelope reaches the handler with `_args` only.
	f = newMWFixture(t, "policy.set")
	const args = `{"direction":"loosen"}`
	nonce := f.challenge(t, args)
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, nonce)); err != nil {
		t.Fatal(err)
	}
	if string(f.gotArgs) != args {
		t.Errorf("handler params = %s, want only the unwrapped %s", f.gotArgs, args)
	}
}
