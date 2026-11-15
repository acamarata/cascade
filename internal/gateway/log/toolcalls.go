// Package log provides internal counters for tool call tracking.
package log

import "encoding/json"

// ToolCall represents a single tool invocation with a name and JSON arguments.
type ToolCall struct {
	// Name is the identifier of the tool being called.
	Name string
	// Args holds the raw JSON arguments for the tool call.
	Args json.RawMessage
}

// ToolCounter tracks tool call statistics and a run cap allowance.
type ToolCounter struct {
	// Stats counts the number of valid tool calls recorded.
	Stats int
	// Cap is the run cap allowance, which is never modified by recording calls.
	Cap int
}

// NewToolCounter creates a new ToolCounter with the given initial cap limit.
func NewToolCounter(limit int) *ToolCounter {
	return &ToolCounter{Cap: limit}
}

// RecordToolCall records a tool call. It returns true if the call is valid,
// meaning Name is non-empty and Args is a valid JSON object. A valid call
// increments Stats but never changes Cap. An invalid call increments neither.
func RecordToolCall(c *ToolCounter, call ToolCall) bool {
	if call.Name == "" {
		return false
	}
	if !json.Valid(call.Args) {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal(call.Args, &obj); err != nil {
		return false
	}
	c.Stats++
	return true
}
