// Package executive defines the normalized executive envelope.
package executive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Envelope is the normalized executive event carrying the R-21.37 fields.
type Envelope struct {
	// EventID uniquely identifies this event.
	EventID string `json:"event_id"`
	// Kind is the closed exec.* event kind.
	Kind Kind `json:"kind"`
	// StreamID identifies the stream scope.
	StreamID StreamID `json:"stream_id"`
	// StreamSeq is the server-allocated per-stream monotonic sequence number.
	StreamSeq StreamSeq `json:"stream_seq"`
	// RunID identifies the run scope.
	RunID RunID `json:"run_id"`
	// ProjectID identifies the project scope.
	ProjectID ProjectID `json:"project_id"`
	// Producer identifies the component that produced the event.
	Producer Producer `json:"producer"`
	// PayloadVersion identifies the schema version of the payload.
	PayloadVersion PayloadVersion `json:"payload_version"`
	// Payload holds the raw event payload bytes.
	Payload Payload `json:"payload"`
	// OccurredAt records when the event occurred.
	OccurredAt time.Time `json:"occurred_at"`
}

// IdempotencyKey returns the delivery key formatted as stream_id:sequence:runtime_session_id.
func (e *Envelope) IdempotencyKey(sessionID string) string {
	return fmt.Sprintf("%s:%d:%s", e.StreamID, e.StreamSeq, sessionID)
}

// MarshalJSON implements json.Marshaler for Payload, emitting raw JSON.
func (p Payload) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("null"), nil
	}
	return []byte(p), nil
}

// UnmarshalJSON implements json.Unmarshaler for Payload, treating it as raw JSON.
func (p *Payload) UnmarshalJSON(data []byte) error {
	if p == nil {
		return errors.New("executive.Payload: UnmarshalJSON on nil pointer")
	}
	*p = append((*p)[0:0], data...)
	return nil
}

var (
	errDuplicateKey = errors.New("duplicate key in envelope JSON")
	errMalformed    = errors.New("malformed envelope JSON")
)

// DecodeEnvelope performs strict decoding of an envelope from JSON bytes.
// It rejects duplicate keys and malformed input.
func DecodeEnvelope(data []byte) (*Envelope, error) {
	if err := checkDuplicateKeys(data); err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var env Envelope
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformed, err)
	}

	if _, err := ParseExecKind(string(env.Kind)); err != nil {
		return nil, fmt.Errorf("%w: invalid kind %q", errMalformed, env.Kind)
	}

	return &env, nil
}

func checkDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return walkForDuplicates(dec)
}

// walkForDuplicates reads one JSON value from dec and refuses a repeated
// object key at any depth. A read error is malformed input, never a pass:
// swallowing it let a truncated array spin More() forever.
func walkForDuplicates(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: %v", errMalformed, err)
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return fmt.Errorf("%w: %v", errMalformed, err)
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("%w: expected string key", errMalformed)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("%w: %q", errDuplicateKey, key)
			}
			seen[key] = struct{}{}
			if err := walkForDuplicates(dec); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("%w: %v", errMalformed, err)
		}
	case '[':
		for dec.More() {
			if err := walkForDuplicates(dec); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("%w: %v", errMalformed, err)
		}
	}
	return nil
}
