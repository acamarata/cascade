package resume

// Purpose: unit coverage for the fan-out record writer (cursor_write.go)
//   and the shared cursor fixture every resume test builds its cursors
//   through, so no test hand-writes a cursor shape the writer cannot emit.
// SPORT: internal.fleet.resume.FanOutStore/ADDED (P1-CORE-19).

import (
	"encoding/json"
	"testing"
)

// cursorPayloadFor renders a fan-out cursor through the production
// encoder (encodeCursor), the one shape WriteCursor appends.
func cursorPayloadFor(t *testing.T, fanoutID, taskID string, legs int) json.RawMessage {
	t.Helper()
	p, err := encodeCursor(FanOutCursor{FanOutID: fanoutID, TaskID: taskID, Legs: legs, RequestKey: fanoutID})
	if err != nil {
		t.Fatalf("encodeCursor: %v", err)
	}
	return p
}
