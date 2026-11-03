package antigravity

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// FuzzAntigravityOutput: decoding never panics, fails only with the four
// typed refusals by identity, and a filter that refuses a marker byte
// never lets that byte reach an event or Output.
func FuzzAntigravityOutput(f *testing.F) {
	f.Add([]byte(`{"event":"init","conversation_id":"c","init":{}}` + "\n" +
		`{"event":"step_update","step_update":{"conversation_id":"c","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"ok"}}`))
	f.Add([]byte("ok\n"))
	f.Fuzz(func(t *testing.T, stream []byte) {
		refuseX := func(b []byte) ([]byte, error) {
			if bytes.IndexByte(b, 'X') >= 0 {
				return nil, errors.New("refused")
			}
			return b, nil
		}
		d, err := NewDecoder(refuseX)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(stream, []byte("\n")) {
			ev, err := d.Decode(line)
			if err != nil {
				checkRefusal(t, err)
				if ev != (Event{}) || d.Output() != "" {
					t.Fatalf("failed decode leaked event %+v output %q", ev, d.Output())
				}
				continue
			}
			checkEvent(t, ev)
			if strings.Contains(ev.Text, "X") || strings.Contains(d.Output(), "X") {
				t.Fatal("a refused chunk passed the filter")
			}
		}
	})
}

func checkRefusal(t *testing.T, err error) {
	t.Helper()
	for _, s := range []error{ErrMalformedEvent, ErrInterfaceAbsent, provider.ErrHarnessIncompatible, provider.ErrEgressSubstitutionFailed} {
		if isSentinel(err, s) {
			return
		}
	}
	t.Fatalf("untyped refusal %v", err)
}

func checkEvent(t *testing.T, ev Event) {
	t.Helper()
	switch ev.Type {
	case EventInit, EventStepUpdate, EventResult:
	default:
		t.Fatalf("unclassified event %q", ev.Type)
	}
	if ev.ConversationID == "" {
		t.Fatal("an event without a conversation id")
	}
}
