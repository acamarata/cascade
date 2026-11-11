package opencode

// Purpose: decode the native run event union and retain its filtered bytes.
// Inputs: one JSON line. Outputs: one normalized event or a typed refusal.
// Constraints: the mapping is closed; native timestamps never drive ordering.

import (
	"encoding/json"

	"github.com/acamarata/cascade/pkg/provider"
)

// eventKinds is the run fixture's type inventory, without speculative rows.
var eventKinds = map[string]provider.DriverEventKind{
	"step_start":  provider.DriverEventStatus,
	"text":        provider.DriverEventStdout,
	"step_finish": provider.DriverEventDone,
}

type nativeFrame struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		ID     string `json:"id"`
		Text   string `json:"text"`
		Reason string `json:"reason"`
	} `json:"part"`
}

func decodeEvent(raw []byte, seq uint64, tier provider.DataClass) (provider.DriverEvent, string, error) {
	var f nativeFrame
	if json.Unmarshal(raw, &f) != nil || f.Type == "" {
		return provider.DriverEvent{}, "", ErrMalformedEvent
	}
	k, ok := eventKinds[f.Type]
	if !ok {
		return provider.DriverEvent{}, "", ErrUnknownEventKind
	}
	e := provider.DriverEvent{Kind: k, Seq: seq, DataClass: tier, Text: f.Part.Text, Native: &provider.NativeEvent{Type: f.Type, Raw: append([]byte(nil), raw...)}}
	if f.Part.ID != "" {
		e.DedupKey = f.Type + ":" + f.Part.ID
	}
	if k == provider.DriverEventStatus {
		e.State = provider.AgentRunRunning
	}
	if k == provider.DriverEventDone {
		if f.Part.Reason != "stop" {
			return provider.DriverEvent{}, "", ErrMalformedEvent
		}
		e.State = provider.AgentRunAccepted
	}
	return e, f.SessionID, nil
}
