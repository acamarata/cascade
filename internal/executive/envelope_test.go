package executive

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestEXE01LegacyAC3 verifies that every envelope carries the R-21.37 fields.
func TestEXE01LegacyAC3(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	env := Envelope{
		EventID:        "evt-1",
		Kind:           KindTaskStarted,
		StreamID:       "stream-1",
		StreamSeq:      42,
		RunID:          "run-1",
		ProjectID:      "proj-1",
		Producer:       "producer-1",
		PayloadVersion: 1,
		Payload:        Payload(`{"key":"value"}`),
		OccurredAt:     now,
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	assertEnvelopeFields(t, got, env)

	wantKey := "stream-1:42:sess-1"
	if k := got.IdempotencyKey("sess-1"); k != wantKey {
		t.Errorf("IdempotencyKey = %q, want %q", k, wantKey)
	}
}

// TestDecodeEnvelopeRejectsDuplicateKeys ensures strict decoding rejects duplicate keys.
func TestDecodeEnvelopeRejectsDuplicateKeys(t *testing.T) {
	input := `{"event_id":"1","kind":"exec.task.started","event_id":"2"}`
	_, err := DecodeEnvelope([]byte(input))
	if err == nil {
		t.Fatal("expected error for duplicate keys")
	}
	if !errors.Is(err, errDuplicateKey) {
		t.Errorf("err = %v, want %v", err, errDuplicateKey)
	}
}

// TestDecodeEnvelopeRejectsMalformed ensures strict decoding rejects malformed JSON.
func TestDecodeEnvelopeRejectsMalformed(t *testing.T) {
	input := `{not valid json`
	_, err := DecodeEnvelope([]byte(input))
	if err == nil {
		t.Fatal("expected error for malformed input")
	}
	if !errors.Is(err, errMalformed) {
		t.Errorf("err = %v, want %v", err, errMalformed)
	}
}

// assertEnvelopeFields compares every R-21.37 field of got against want.
func assertEnvelopeFields(t *testing.T, got *Envelope, want Envelope) {
	t.Helper()
	if got.EventID != want.EventID {
		t.Errorf("EventID = %q, want %q", got.EventID, want.EventID)
	}
	if got.Kind != want.Kind {
		t.Errorf("Kind = %q, want %q", got.Kind, want.Kind)
	}
	if got.StreamID != want.StreamID {
		t.Errorf("StreamID = %q, want %q", got.StreamID, want.StreamID)
	}
	if got.StreamSeq != want.StreamSeq {
		t.Errorf("StreamSeq = %d, want %d", got.StreamSeq, want.StreamSeq)
	}
	if got.RunID != want.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, want.RunID)
	}
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.Producer != want.Producer {
		t.Errorf("Producer = %q, want %q", got.Producer, want.Producer)
	}
	if got.PayloadVersion != want.PayloadVersion {
		t.Errorf("PayloadVersion = %d, want %d", got.PayloadVersion, want.PayloadVersion)
	}
	if string(got.Payload) != string(want.Payload) {
		t.Errorf("Payload = %s, want %s", got.Payload, want.Payload)
	}
	if !got.OccurredAt.Equal(want.OccurredAt) {
		t.Errorf("OccurredAt = %v, want %v", got.OccurredAt, want.OccurredAt)
	}
}

// TestDecodeEnvelopeRefusals covers every strict-decoding refusal: an
// unknown kind, an unknown field, a nested or array-held duplicate key and
// truncated input; a non-object top level is malformed, never accepted.
func TestDecodeEnvelopeRefusals(t *testing.T) {
	cases := map[string]struct {
		in   string
		want error
	}{
		"unknown kind":       {`{"event_id":"e","kind":"exec.nope"}`, errMalformed},
		"unknown field":      {`{"event_id":"e","kind":"exec.task.started","extra":1}`, errMalformed},
		"nested duplicate":   {`{"event_id":"e","kind":"exec.task.started","payload":{"a":1,"a":2}}`, errDuplicateKey},
		"array duplicate":    {`{"event_id":"e","kind":"exec.task.started","payload":[{"a":1,"a":2}]}`, errDuplicateKey},
		"truncated object":   {`{"event_id":"e",`, errMalformed},
		"truncated array":    {`{"payload":[1,`, errMalformed},
		"non-object scalar":  {`42`, errMalformed},
		"empty input":        {``, errMalformed},
		"array payload kept": {`{"event_id":"e","kind":"exec.task.started","payload":[1,2]}`, nil},
	}
	for name, tc := range cases {
		_, err := DecodeEnvelope([]byte(tc.in))
		if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

// TestPayloadRawJSON: a nil Payload marshals as null, a nil *Payload
// refuses UnmarshalJSON, and a decoded Payload keeps its raw bytes.
func TestPayloadRawJSON(t *testing.T) {
	if b, err := Payload(nil).MarshalJSON(); err != nil || string(b) != "null" {
		t.Fatalf("nil MarshalJSON = %q, %v", b, err)
	}
	var nilp *Payload
	if err := nilp.UnmarshalJSON([]byte(`{}`)); err == nil {
		t.Fatal("UnmarshalJSON on a nil *Payload returned no error")
	}
	var p Payload
	if err := p.UnmarshalJSON([]byte(`{"k":1}`)); err != nil || string(p) != `{"k":1}` {
		t.Fatalf("UnmarshalJSON = %q, %v", p, err)
	}
}
