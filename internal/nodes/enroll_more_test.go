package nodes

import "testing"

// TestDecodeEnrollHandshakePayload_MissingHostFields isolates the
// missing-host/host_key_fingerprint refusal: enroll_test.go's malformed
// table only exercises the EARLIER missing-node_id/pubkey check (both
// checks return before this one when node_id is also absent), so this
// branch needs a payload where node_id/pubkey ARE present.
func TestDecodeEnrollHandshakePayload_MissingHostFields(t *testing.T) {
	raw := []byte(`{"node_id":"n1","node_pubkey_b64":"cGxhY2Vob2xkZXI=","node_signature_b64":"c2ln"}`)
	_, err := DecodeEnrollHandshakePayload(raw)
	if err == nil {
		t.Fatal("expected refusal for a payload missing host/host_key_fingerprint")
	}
}

// TestDecodeEnrollHandshakePayload_MissingSignature isolates the
// missing-node_signature_b64 refusal, similarly requiring every earlier
// field to be present so the decoder reaches this specific check.
func TestDecodeEnrollHandshakePayload_MissingSignature(t *testing.T) {
	raw := []byte(`{"node_id":"n1","node_pubkey_b64":"cGxhY2Vob2xkZXI=","host":"worker@host1","host_key_fingerprint":"fp"}`)
	_, err := DecodeEnrollHandshakePayload(raw)
	if err == nil {
		t.Fatal("expected refusal for a payload missing node_signature_b64")
	}
}
