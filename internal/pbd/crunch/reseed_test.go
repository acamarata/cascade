package crunch

import (
	"reflect"
	"testing"
)

// TestReseedPendingFalseSkipsRecap ensures that when reseedPending is false,
// the payload is returned completely unchanged, preserving any preexisting recap.
func TestReseedPendingFalseSkipsRecap(t *testing.T) {
	tests := []struct {
		name    string
		payload CodePayload
		history []HistoryItem
	}{
		{
			name: "absent recap",
			payload: CodePayload{
				Text: "hello",
			},
			history: []HistoryItem{
				{Role: "user", Text: "hi"},
				{Role: "assistant", Text: "hello"},
			},
		},
		{
			name: "preexisting recap",
			payload: CodePayload{
				Text: "hello",
				ReseedRecap: ReseedRecap{
					{Role: "user", Text: "old recap"},
				},
			},
			history: []HistoryItem{
				{Role: "user", Text: "hi"},
				{Role: "assistant", Text: "hello"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a deep copy to compare against later, since we expect it to be strictly identical
			expected := CodePayload{
				Text:        tt.payload.Text,
				ReseedRecap: append(ReseedRecap(nil), tt.payload.ReseedRecap...),
			}

			got := InjectReseedRecap(tt.payload, false, tt.history)

			if !reflect.DeepEqual(got, expected) {
				t.Errorf("InjectReseedRecap() with pending=false mutated payload.\ngot = %#v\nwant = %#v", got, expected)
			}
		})
	}
}
