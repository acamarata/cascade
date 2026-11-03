// Package antigravity holds the antigravity (agy) headless driver pieces:
// the stream-json output decoder, its egress filter seam and the protocol
// pin.
package antigravity

// Purpose: decode agy --output-format stream-json lines (one JSON object
// per line, keyed by "event") into typed events, passing every text
// chunk through the job's OutputFilter before anything is kept.
// Inputs: raw stdout lines from the child, in order.
// Outputs: an Event per line and the filtered agent text so far, or a
// typed refusal: ErrMalformedEvent, ErrInterfaceAbsent,
// provider.ErrHarnessIncompatible or provider.ErrEgressSubstitutionFailed.
// Constraints: fail closed. A filter error withholds the chunk and every
// later one, Output then returns "", and the decoder stays failed. The
// first line must be the init event. Unknown fields are ignored; an
// unknown event name is protocol drift.
// SPORT: providers.agents.antigravity/ADD (P1-AGT-07).

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The revision 1 event names.
const (
	EventInit       = "init"
	EventStepUpdate = "step_update"
	EventResult     = "result"
)

// stepTypeAgentResponse marks the step whose text deltas are the reply.
const stepTypeAgentResponse = "agent_response"

// ErrMalformedEvent refuses a line that is not a well-formed revision 1
// stream-json event.
var ErrMalformedEvent = cascade.New(cascade.KindIntegrity, "antigravity: malformed stream-json event")

// errNilFilter refuses a decoder built without an OutputFilter.
var errNilFilter = cascade.New(cascade.KindInvalidInput, "antigravity: output filter is required")

// Event is one decoded stream-json line. Text is the filtered
// text_delta (step_update) or response (result); raw unfiltered text is
// never exposed.
type Event struct {
	Type           string
	ConversationID string
	StepIndex      int
	State          string
	StepType       string
	Status         string
	Text           string
}

type wireStep struct {
	ConversationID string `json:"conversation_id"`
	StepIndex      *int   `json:"step_index"`
	State          string `json:"state"`
	StepType       string `json:"step_type"`
	TextDelta      string `json:"text_delta"`
}

type wireResult struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	Response       string `json:"response"`
}

type wireEvent struct {
	Event          string          `json:"event"`
	ConversationID string          `json:"conversation_id"`
	Init           json.RawMessage `json:"init"`
	Step           *wireStep       `json:"step_update"`
	Result         *wireResult     `json:"result"`
}

// Decoder turns a child's stream-json stdout into filtered events. It is
// not safe for concurrent use.
type Decoder struct {
	filter  provider.OutputFilter
	started bool
	failed  error
	out     strings.Builder
}

// NewDecoder returns a decoder that passes every text chunk through
// filter. A nil filter is refused (the driver rule: no filter, no child).
func NewDecoder(filter provider.OutputFilter) (*Decoder, error) {
	if filter == nil {
		return nil, errNilFilter
	}
	return &Decoder{filter: filter}, nil
}

// Decode classifies one line and filters its text. Any error is sticky:
// every later call returns it unchanged.
func (d *Decoder) Decode(line []byte) (Event, error) {
	if d.failed != nil {
		return Event{}, d.failed
	}
	ev, err := d.decode(line)
	if err != nil {
		d.failed = err
		d.out.Reset()
		return Event{}, err
	}
	return ev, nil
}

func (d *Decoder) decode(line []byte) (Event, error) {
	ev, err := parseEvent(line)
	if !d.started {
		if err != nil || ev.Type != EventInit {
			return Event{}, ErrInterfaceAbsent
		}
		d.started = true
		return ev, nil
	}
	if err != nil {
		return Event{}, err
	}
	if ev.Type == EventInit {
		return Event{}, malformed("a second init event")
	}
	if ev.Text == "" {
		return ev, nil
	}
	clean, ferr := d.filter([]byte(ev.Text))
	if ferr != nil {
		// The filter error is dropped on purpose: it may quote the chunk.
		return Event{}, provider.ErrEgressSubstitutionFailed
	}
	ev.Text = string(clean)
	if ev.Type == EventStepUpdate && ev.StepType == stepTypeAgentResponse {
		d.out.WriteString(ev.Text)
	}
	return ev, nil
}

// Output returns the filtered agent reply decoded so far, or "" once the
// decoder has failed.
func (d *Decoder) Output() string {
	if d.failed != nil {
		return ""
	}
	return d.out.String()
}

// Err returns the sticky failure, or nil.
func (d *Decoder) Err() error { return d.failed }

// parseEvent decodes one line without filtering.
func parseEvent(line []byte) (Event, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return Event{}, malformed("not a JSON object")
	}
	var w wireEvent
	if err := json.Unmarshal(line, &w); err != nil {
		return Event{}, malformed("not JSON")
	}
	switch w.Event {
	case "":
		return Event{}, malformed("no event name")
	case EventInit:
		if w.ConversationID == "" || len(w.Init) == 0 || w.Init[0] != '{' {
			return Event{}, malformed("init without conversation_id or init object")
		}
		return Event{Type: EventInit, ConversationID: w.ConversationID}, nil
	case EventStepUpdate:
		return stepEvent(w.Step)
	case EventResult:
		return resultEvent(w.Result)
	default:
		return Event{}, cascade.Wrapf(cascade.KindConflict, provider.ErrHarnessIncompatible,
			"antigravity: event %q is not in protocol revision %s", w.Event, ProtocolRevision)
	}
}

func stepEvent(s *wireStep) (Event, error) {
	if s == nil || s.ConversationID == "" || s.StepIndex == nil || *s.StepIndex < 0 || s.State == "" {
		return Event{}, malformed("step_update without conversation_id, step_index or state")
	}
	return Event{
		Type: EventStepUpdate, ConversationID: s.ConversationID, StepIndex: *s.StepIndex,
		State: s.State, StepType: s.StepType, Text: s.TextDelta,
	}, nil
}

func resultEvent(r *wireResult) (Event, error) {
	if r == nil || r.ConversationID == "" || r.Status == "" {
		return Event{}, malformed("result without conversation_id or status")
	}
	return Event{Type: EventResult, ConversationID: r.ConversationID, Status: r.Status, Text: r.Response}, nil
}

func malformed(why string) error {
	return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEvent, "antigravity: %s", why)
}
