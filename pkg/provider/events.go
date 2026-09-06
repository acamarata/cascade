// Purpose: the typed streaming-event payloads ModelProvider.Stream delivers
//   to its StreamSink (types.go) - the "typed events" half of the plan
//   parenthetical's stream verb.
// Inputs: none at this layer - these are data shapes, not behavior.
// Outputs: none.
// Constraints: these events are an in-process Go contract between a
//   ModelProvider driver and its caller (the conductor); they are never
//   marshaled directly onto the daemon's own SSE wire (R-21.264: "SSE is
//   delivery only" - the bridge from these events onto GET /events belongs
//   to whatever ticket owns that transport, not this one). Exactly one
//   terminal event (done or error) per Stream call, per R-21.217's
//   sync.Once latch discipline - StreamEventKind alone does not enforce
//   this; the driver implementation must.
// SPORT: pkg.provider.modelprovider-contract/ADD (P1-E10-W3-S19-T1).

package provider

// StreamEventKind discriminates one StreamEvent's payload.
type StreamEventKind uint8

// The five StreamEventKind members.
const (
	// StreamEventUnknown is the zero value: never a kind a driver
	// deliberately emits. A sink that receives it should treat the event
	// as malformed.
	StreamEventUnknown StreamEventKind = iota
	// StreamEventDelta carries one incremental text fragment.
	StreamEventDelta
	// StreamEventToolCall carries one tool-invocation request the model
	// emitted mid-stream.
	StreamEventToolCall
	// StreamEventUsage carries a usage update, typically emitted once near
	// the end of the stream.
	StreamEventUsage
	// StreamEventDone is the terminal success event: exactly one per
	// Stream call that completes normally.
	StreamEventDone
	// StreamEventError is the terminal failure event: exactly one per
	// Stream call that ends in error, carrying that error in Err.
	StreamEventError
)

// streamEventKindNames is indexed by StreamEventKind value; String uses it
// in place of a switch so the exhaustive linter never applies here.
var streamEventKindNames = [...]string{
	"unknown", "delta", "tool_call", "usage", "done", "error",
}

// Valid reports whether k is one of the six declared StreamEventKind
// members (including the zero-value StreamEventUnknown).
func (k StreamEventKind) Valid() bool {
	return k <= StreamEventError
}

// String returns the kind's stable lowercase name, or
// "invalid-stream-event-kind" for a value outside the declared set.
func (k StreamEventKind) String() string {
	if !k.Valid() {
		return "invalid-stream-event-kind"
	}
	return streamEventKindNames[k]
}

// ToolCall is one tool-invocation request a model emitted mid-stream (or as
// part of a non-streaming ChatResponse's FinishReason == "tool_call" leg).
// Arguments is passed through as the provider's own raw JSON-shaped map -
// this contract does not interpret tool schemas.
type ToolCall struct {
	// Name is the tool's name as the model invoked it.
	Name string
	// Arguments holds the call's arguments, keyed by parameter name.
	Arguments map[string]any
}

// StreamEvent is one event ModelProvider.Stream delivers to its StreamSink.
// Exactly one field group is populated, selected by Kind; a sink that reads
// a field not implied by Kind sees its zero value.
type StreamEvent struct {
	// Kind selects which field below is populated.
	Kind StreamEventKind
	// Delta holds the text fragment when Kind == StreamEventDelta.
	Delta string
	// ToolCall holds the invocation when Kind == StreamEventToolCall.
	ToolCall ToolCall
	// Usage holds the usage update when Kind == StreamEventUsage or
	// StreamEventDone.
	Usage Usage
	// Err holds the terminal failure when Kind == StreamEventError.
	Err error
}
