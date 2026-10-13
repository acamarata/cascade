package provider

// Purpose: DriverEvent, AgentEvent and their normalization (R-21.158): the
//
//	raw union a driver emits, the pure DTO internal/jobs converts to the
//	AC job event set, and the per-job ordering/dedup guarantees that make
//	the pkg-level event stream trustworthy without trusting any one
//	driver's own sequencing.
//
// Inputs: a raw DriverEvent per call.
// Outputs: a normalized AgentEvent, or a typed error.
// Constraints: NormalizeEvent itself is the STATELESS variant-mapping
//
//	function the full_desc names — an unknown DriverEventKind always
//	returns a typed error, never (AgentEvent{}, nil). Sequence-regression
//	and dedup enforcement are inherently cross-call state, so they live on
//	EventNormalizer (see this ticket's journal for the contradiction: the
//	tasks list asks NormalizeEvent itself to reject a Seq regression,
//	which no stateless function can do across two separate calls).
//
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-E30-W6-S61-T1).

import "github.com/acamarata/cascade/pkg/cascade"

// DriverEventKind discriminates one DriverEvent's payload — the raw union
// a driver's stdio/control channel emits before normalization.
type DriverEventKind uint8

// The four DriverEventKind members. The zero value is deliberately not
// DriverEventStdout or any other real member.
const (
	// DriverEventUnknown is the zero value: never a kind a driver
	// deliberately emits. NormalizeEvent always refuses it.
	DriverEventUnknown DriverEventKind = iota
	// DriverEventStdout carries one raw output line.
	DriverEventStdout
	// DriverEventStatus carries a run-state change.
	DriverEventStatus
	// DriverEventError carries a terminal failure.
	DriverEventError
	// DriverEventDone is the terminal success event.
	DriverEventDone
)

// DriverEvent is one raw event as a driver emits it, before
// normalization. Exactly one field group is meaningful, selected by Kind.
type DriverEvent struct {
	Kind      DriverEventKind
	Seq       uint64
	DedupKey  string
	Text      string
	State     AgentRunState
	Err       error
	DataClass DataClass
	// Native is the harness's undecoded per-event frame, nil when the driver
	// has no per-event native stream.
	Native *NativeEvent
}

// NativeEvent is one event in the harness's own stream: its type name and the
// undecoded line or frame. Consumers that need harness detail read Raw; the
// closed fields of AgentEvent carry the normalized meaning.
type NativeEvent struct {
	Type string
	// Raw is the undecoded native line or frame, exactly as received.
	Raw []byte
}

// AgentEvent is the pure pkg DTO NormalizeEvent produces — the shape
// internal/jobs converts to the AC job event set defined by AC/S-59.T1.
// It carries the fields DriverEvent does, plus the pinned ProtocolVersion
// for the job (R-21.158).
type AgentEvent struct {
	Kind     DriverEventKind
	Seq      uint64
	DedupKey string
	Text     string
	State    AgentRunState
	Err      error
	// DataClass is the joined sensitivity class of this event (R-21.143).
	// Required.
	DataClass DataClass
	// Protocol is the ProtocolVersion pinned for this job at Negotiate
	// time; every AgentEvent for a job carries the same value.
	Protocol ProtocolVersion
	// Native is the driver's undecoded native event; nil when the driver has
	// no per-event native stream (claude headless json mode).
	Native *NativeEvent
}

// NormalizeEvent maps one raw DriverEvent to its pure AgentEvent DTO. It is
// stateless: two calls with the same input always produce the same
// output, and it never consults or updates any cross-call state. An
// unknown Kind returns a typed error rather than a permissive pass-through
// — internal/jobs never sees a malformed event silently accepted.
func NormalizeEvent(raw DriverEvent) (AgentEvent, error) {
	switch raw.Kind {
	case DriverEventStdout, DriverEventStatus, DriverEventError, DriverEventDone:
		return AgentEvent{
			Kind:      raw.Kind,
			Seq:       raw.Seq,
			DedupKey:  raw.DedupKey,
			Text:      raw.Text,
			State:     raw.State,
			Err:       raw.Err,
			DataClass: JoinSensitivity(raw.DataClass, SensitivityPublic),
			Native:    cloneNative(raw.Native),
		}, nil
	case DriverEventUnknown:
		fallthrough
	default:
		return AgentEvent{}, cascade.New(cascade.KindInvalidInput, "provider: unknown driver event variant")
	}
}

// cloneNative returns a deep copy of n so a normalized event never aliases
// the driver's buffer; nil stays nil.
func cloneNative(n *NativeEvent) *NativeEvent {
	if n == nil {
		return nil
	}
	return &NativeEvent{Type: n.Type, Raw: append([]byte(nil), n.Raw...)}
}

// EventNormalizer enforces the R-21.158 per-job ordering and dedup
// invariants across a sequence of Normalize calls — the stateful
// complement to the stateless package-level NormalizeEvent.
type EventNormalizer struct {
	lastSeq uint64
	started bool
	seen    map[string]struct{}
}

// NewEventNormalizer returns a normalizer with no events observed yet.
func NewEventNormalizer() *EventNormalizer {
	return &EventNormalizer{seen: make(map[string]struct{})}
}

// Normalize maps raw through NormalizeEvent, then applies dedup and
// ordering: a repeated DedupKey is dropped (keep is false, err is nil), a
// Seq at or below the last accepted Seq returns ErrOutOfOrderEvent, and an
// unknown variant returns NormalizeEvent's own typed error.
func (n *EventNormalizer) Normalize(raw DriverEvent) (ev AgentEvent, keep bool, err error) {
	ev, err = NormalizeEvent(raw)
	if err != nil {
		return AgentEvent{}, false, err
	}
	if ev.DedupKey != "" {
		if _, dup := n.seen[ev.DedupKey]; dup {
			return AgentEvent{}, false, nil
		}
	}
	if n.started && ev.Seq <= n.lastSeq {
		return AgentEvent{}, false, ErrOutOfOrderEvent
	}
	if ev.DedupKey != "" {
		n.seen[ev.DedupKey] = struct{}{}
	}
	n.lastSeq = ev.Seq
	n.started = true
	return ev, true, nil
}
