package executive

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EventDTO is the public event surface of an exec.* envelope.
type EventDTO struct {
	// EventID uniquely identifies the event.
	EventID string `json:"event_id"`
	// Kind is the closed exec.* kind.
	Kind Kind `json:"kind"`
	// StreamID identifies the stream scope.
	StreamID StreamID `json:"stream_id"`
	// StreamSeq is the server-allocated per-stream sequence number.
	StreamSeq StreamSeq `json:"stream_seq"`
	// RunID identifies the run scope.
	RunID RunID `json:"run_id"`
	// ProjectID identifies the project scope.
	ProjectID ProjectID `json:"project_id"`
	// Producer identifies the component that produced the event.
	Producer Producer `json:"producer"`
	// PayloadVersion is the payload schema version.
	PayloadVersion PayloadVersion `json:"payload_version"`
	// Payload is the validated payload body.
	Payload json.RawMessage `json:"payload"`
	// OccurredAt records when the event occurred.
	OccurredAt time.Time `json:"occurred_at"`
}

// ExplainDTO is the public explain surface for one event.
type ExplainDTO struct {
	// EventID identifies the explained event.
	EventID string `json:"event_id"`
	// Kind is the explained event's kind.
	Kind Kind `json:"kind"`
	// Reason explains why the event occurred.
	Reason string `json:"reason"`
}

// LogDTO is the public log surface for one event.
type LogDTO struct {
	// EventID identifies the logged event.
	EventID string `json:"event_id"`
	// Kind is the logged event's kind.
	Kind Kind `json:"kind"`
	// Message is the log line.
	Message string `json:"message"`
}

var publicDTOTypes = []reflect.Type{
	reflect.TypeOf(EventDTO{}),
	reflect.TypeOf(ExplainDTO{}),
	reflect.TypeOf(LogDTO{}),
}

// PublicDTOTypes returns a fresh copy of the types MarshalPublic accepts.
func PublicDTOTypes() []reflect.Type {
	return append([]reflect.Type(nil), publicDTOTypes...)
}

// NewEventDTO validates env's payload and copies env into an EventDTO.
func NewEventDTO(env *Envelope) (EventDTO, error) {
	if env == nil {
		return EventDTO{}, cascade.New(cascade.KindInvalidInput, "executive: nil envelope")
	}
	if err := ValidatePayload(env.Kind, env.PayloadVersion, env.Payload); err != nil {
		return EventDTO{}, err
	}
	return EventDTO{
		EventID:        env.EventID,
		Kind:           env.Kind,
		StreamID:       env.StreamID,
		StreamSeq:      env.StreamSeq,
		RunID:          env.RunID,
		ProjectID:      env.ProjectID,
		Producer:       env.Producer,
		PayloadVersion: env.PayloadVersion,
		Payload:        append(json.RawMessage(nil), env.Payload...),
		OccurredAt:     env.OccurredAt,
	}, nil
}

// isPublicDTO reports whether dto's dynamic type is one of PublicDTOTypes.
func isPublicDTO(dto any) bool {
	t := reflect.TypeOf(dto)
	for _, p := range publicDTOTypes {
		if t == p {
			return true
		}
	}
	return false
}

// MarshalPublic serializes one public DTO and refuses the bytes when det
// finds a credential in them. A type outside PublicDTOTypes is
// KindInvalidInput; an EventDTO's payload is validated first. On any
// refusal the returned bytes are nil.
func MarshalPublic(det *secrets.Detector, dto any) ([]byte, error) {
	if !isPublicDTO(dto) {
		return nil, cascade.New(cascade.KindInvalidInput, fmt.Sprintf("executive: %T is not a public DTO", dto))
	}
	if ev, ok := dto.(EventDTO); ok {
		if err := ValidatePayload(ev.Kind, ev.PayloadVersion, ev.Payload); err != nil {
			return nil, err
		}
	}
	b, err := json.Marshal(dto)
	if err != nil {
		return nil, cascade.New(cascade.KindInvalidInput, "executive: public DTO does not marshal")
	}
	if err := GuardSerialized(det, b); err != nil {
		return nil, err
	}
	return b, nil
}
